package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/belai/internal/budget"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/tui/components"
)

// budgetsViewState is the token budgets screen's state. mode is "" when
// browsing, or one step of adding ("add-target", "add-scope", "add-tokens"),
// or "edit" while changing the selected budget's allowance.
type budgetsViewState struct {
	selected int
	mode     string
	draft    config.TokenBudget
	errorMsg string
}

// openBudgets pushes the token budgets screen.
func (a *App) openBudgets() tea.Cmd { return a.push(viewBudgets) }

func (a *App) enterBudgets() tea.Cmd {
	a.budgetsState = budgetsViewState{}
	if a.budgets != nil {
		a.budgets.Refresh(0)
	}
	return nil
}

// budgetRows lists every budget: the selected provider+model's first, then
// the rest by provider/model, each model's in scope order.
func (a *App) budgetRows() []config.TokenBudget {
	rows := append([]config.TokenBudget(nil), a.settings.TokenBudgets...)
	cur := config.ModelKey(a.cfg.Provider, a.cfg.Model)
	scopeRank := map[string]int{}
	for i, s := range config.BudgetScopes {
		scopeRank[s] = i
	}
	sort.SliceStable(rows, func(i, j int) bool {
		ki, kj := rows[i].Key(), rows[j].Key()
		if (ki == cur) != (kj == cur) {
			return ki == cur
		}
		if ki != kj {
			return ki < kj
		}
		return scopeRank[rows[i].Scope] < scopeRank[rows[j].Scope]
	})
	return rows
}

func (a *App) budgetsView() string {
	w := a.contentWidth()
	var b strings.Builder
	b.WriteString(components.SectionHeader("Token budgets", "esc back", w))
	path, _ := config.GlobalSettingsPath()
	b.WriteString(components.Chip("global", components.ColorTealSoft) + "  " + components.MutedStyle.Render(path) + "\n\n")

	rows := a.budgetRows()
	if len(rows) == 0 {
		b.WriteString(components.MutedStyle.Render("No budgets yet. Press a to add one for "+
			config.ModelKey(a.cfg.Provider, a.cfg.Model)+".") + "\n")
	}
	cur := config.ModelKey(a.cfg.Provider, a.cfg.Model)
	now := time.Now()
	lastKey := ""
	for i, row := range rows {
		if row.Key() != lastKey {
			if lastKey != "" {
				b.WriteString("\n")
			}
			head := row.Key()
			if head == cur {
				head += components.MutedStyle.Render("  (selected)")
			}
			b.WriteString(components.EmphStyle.Render(head) + "\n")
			lastKey = row.Key()
		}
		var used int64
		if a.budgets != nil {
			used = a.budgets.Used(row)
		}
		g := budget.Status(row, used, now)
		fg := toFooterGauge(g)
		timeLeft := "—"
		if g.HasTime() {
			timeLeft = fmt.Sprintf("%s (%d%%)", fg.TimeLeft, g.TimePctLeft)
		}
		style := lipgloss.NewStyle().Foreground(fg.Colour())
		line := fmt.Sprintf("%-8s %9s limit  %9s used  %s  %-14s ",
			row.Scope, humanTokens(row.Tokens), humanTokens(used),
			style.Render(fmt.Sprintf("%3d%% left", g.TokenPctLeft)), timeLeft)
		selected := i == a.budgetsState.selected
		if selected {
			line = components.AccentStyle.Bold(true).Render(fmt.Sprintf("%-8s", row.Scope)) + line[8:]
		}
		b.WriteString(components.Cursor(selected) + line + fg.Bar() + "\n")
	}

	s := a.settings
	b.WriteString("\n" + components.MutedStyle.Render(fmt.Sprintf(
		"footer cycles every %ds · warnings %s · change both in /settings",
		int(s.BudgetCycle().Seconds()), boolLabel(s.BudgetWarnEnabled()))) + "\n")

	if a.budgetsState.errorMsg != "" {
		b.WriteString("\n" + components.DangerStyle.Render("✗ "+a.budgetsState.errorMsg) + "\n")
	}
	switch a.budgetsState.mode {
	case "add-target":
		b.WriteString("\n" + a.renderFieldEditor("provider/model", w) + "\n")
	case "add-scope":
		b.WriteString("\n" + components.EmphStyle.Render(a.budgetsState.draft.Key()) + "  " +
			components.HelpBar("s", "session", "d", "day", "m", "month", "esc", "cancel") + "\n")
	case "add-tokens", "edit":
		d := a.budgetsState.draft
		b.WriteString("\n" + a.renderFieldEditor(fmt.Sprintf("%s budget for %s (e.g. 250k, 1.5M)", d.Scope, d.Key()), w) + "\n")
	default:
		b.WriteString("\n" + components.HelpBar("↑↓", "move", "a", "add", "enter", "edit", "x", "delete", "esc", "back") + "\n")
	}
	return lipgloss.NewStyle().Padding(1).Render(b.String())
}

func (a *App) handleBudgetsKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	st := &a.budgetsState
	rows := a.budgetRows()
	key := m.String()

	switch st.mode {
	case "add-target":
		switch key {
		case "esc":
			a.cancelBudgetEdit()
			return a, nil
		case "enter":
			provider, model, ok := strings.Cut(strings.TrimSpace(a.editor.Value()), "/")
			if !ok || strings.TrimSpace(provider) == "" || strings.TrimSpace(model) == "" {
				st.errorMsg = "enter provider/model, e.g. openrouter/anthropic/claude-sonnet-5"
				return a, nil
			}
			st.draft = config.TokenBudget{Provider: strings.TrimSpace(provider), Model: strings.TrimSpace(model)}
			st.mode, st.errorMsg = "add-scope", ""
			a.editor.Reset()
			return a, nil
		}
		return a, a.editor.Update(m)
	case "add-scope":
		scope := map[string]string{"s": config.BudgetScopeSession, "d": config.BudgetScopeDay, "m": config.BudgetScopeMonth}[key]
		switch {
		case key == "esc":
			a.cancelBudgetEdit()
		case scope != "":
			for _, b := range a.settings.TokenBudgets {
				if b.Key() == st.draft.Key() && b.Scope == scope {
					st.errorMsg = fmt.Sprintf("%s already has a %s budget; select it and press enter to change it", st.draft.Key(), scope)
					return a, nil
				}
			}
			st.draft.Scope = scope
			st.mode, st.errorMsg = "add-tokens", ""
			a.editor.Reset()
			_ = a.editor.Focus()
		}
		return a, nil
	case "add-tokens", "edit":
		switch key {
		case "esc":
			a.cancelBudgetEdit()
			return a, nil
		case "enter":
			n, err := parseBudgetTokens(a.editor.Value())
			if err != nil {
				st.errorMsg = err.Error()
				return a, nil
			}
			d := st.draft
			d.Tokens = n
			if err := a.saveBudget(d); err != nil {
				st.errorMsg = err.Error()
				return a, nil
			}
			a.cancelBudgetEdit()
			a.refreshFooter()
			return a, nil
		}
		return a, a.editor.Update(m)
	}

	switch key {
	case "esc":
		a.pop()
	case "up", "k":
		if st.selected > 0 {
			st.selected--
		}
	case "down", "j":
		if st.selected < len(rows)-1 {
			st.selected++
		}
	case "a":
		st.mode, st.errorMsg = "add-target", ""
		a.editor.Masked = false
		a.editor.SetValue(config.ModelKey(a.cfg.Provider, a.cfg.Model))
		_ = a.editor.Focus()
	case "enter", " ":
		if st.selected < len(rows) {
			st.draft = rows[st.selected]
			st.mode, st.errorMsg = "edit", ""
			a.editor.Masked = false
			a.editor.SetValue(humanTokens(st.draft.Tokens))
			_ = a.editor.Focus()
		}
	case "x":
		if st.selected < len(rows) {
			if err := a.deleteBudget(rows[st.selected]); err != nil {
				st.errorMsg = err.Error()
			} else {
				st.errorMsg = ""
				if st.selected > 0 && st.selected >= len(rows)-1 {
					st.selected--
				}
				a.refreshFooter()
			}
		}
	}
	return a, nil
}

func (a *App) cancelBudgetEdit() {
	a.budgetsState.mode = ""
	a.budgetsState.errorMsg = ""
	a.budgetsState.draft = config.TokenBudget{}
	a.editor.Reset()
}

// saveBudget adds b, or replaces the allowance of the existing budget with the
// same provider, model and scope. Budgets are global: the write always goes to
// the global settings file, whatever scope /settings is on.
func (a *App) saveBudget(b config.TokenBudget) error {
	return a.mutateBudgets(func(list []config.TokenBudget) []config.TokenBudget {
		for i := range list {
			if list[i].Key() == b.Key() && list[i].Scope == b.Scope {
				list[i].Tokens = b.Tokens
				return list
			}
		}
		return append(list, b)
	})
}

func (a *App) deleteBudget(b config.TokenBudget) error {
	return a.mutateBudgets(func(list []config.TokenBudget) []config.TokenBudget {
		out := list[:0]
		for _, x := range list {
			if x.Key() == b.Key() && x.Scope == b.Scope {
				continue
			}
			out = append(out, x)
		}
		return out
	})
}

func (a *App) mutateBudgets(fn func([]config.TokenBudget) []config.TokenBudget) error {
	err := config.Mutate(config.ScopeGlobal, a.workdir, func(s *config.Settings) error {
		s.TokenBudgets = fn(s.TokenBudgets)
		if len(s.TokenBudgets) == 0 {
			s.TokenBudgets = nil
		}
		return config.ValidateTokenBudgets(*s)
	})
	if err != nil {
		return err
	}
	return a.reloadSettings()
}
