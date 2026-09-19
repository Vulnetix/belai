package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/tui/components"
)

// settingsViewState tracks the settings browser UI.
type settingsViewState struct {
	selected int
	scope    config.Scope
	editMode bool
	errorMsg string
}

// settingsRow is one declarative settings-browser row.
type settingsRow struct {
	key   string
	label string
	kind  string   // toggle | choose | text | submenu | pick
	opts  []string // choose options
	value string   // rendered effective value
	src   string   // provenance label
	// disabled greys the row out and makes space/enter skip it. It is how a
	// row that only means something under another row's setting — an effort
	// with reasoning off — stays visible without being changeable.
	disabled bool
}

func (a *App) settingsView() string {
	rows := a.settingsRows()
	w := a.contentWidth()
	var b strings.Builder
	b.WriteString(components.SectionHeader("Settings", "esc back", w))

	path := config.ProjectSettingsPath(a.workdir)
	scope := string(a.settingsState.scope)
	if a.settingsState.scope == config.ScopeGlobal {
		p, _ := config.GlobalSettingsPath()
		path = p
	}
	if scope == "" {
		// The view opens before a scope has been chosen; it writes project.
		scope = string(config.ScopeProject)
	}
	b.WriteString(components.Chip(scope, components.ColorTealSoft) +
		"  " + components.MutedStyle.Render(path) + "\n\n")

	for i, row := range rows {
		selected := i == a.settingsState.selected
		label := fmt.Sprintf("%-20s", row.label)
		value := fmt.Sprintf("%-27s ", row.value)
		if row.kind == "submenu" {
			value = fmt.Sprintf("%-25s → ", row.value)
		}
		if selected {
			label = components.AccentStyle.Bold(true).Render(label)
			value = components.EmphStyle.Render(value)
		} else {
			label = components.MutedStyle.Render(label)
		}
		b.WriteString(components.Cursor(selected) + label + value +
			components.MutedStyle.Render(row.src) + "\n")
	}

	if a.settingsState.errorMsg != "" {
		b.WriteString("\n" + components.DangerStyle.Render("✗ "+a.settingsState.errorMsg) + "\n")
	}

	if a.settingsState.editMode {
		b.WriteString("\n" + a.renderFieldEditor("edit", w) + "\n")
		b.WriteString("\n" + components.HelpBar("enter", "save", "esc", "cancel") + "\n")
	} else {
		b.WriteString("\n" + components.HelpBar(
			"↑↓", "move", "space", "edit/open", "x", "unset", "s", "scope", "esc", "back") + "\n")
	}
	return lipgloss.NewStyle().Padding(1).Render(b.String())
}

func (a *App) settingsRows() []settingsRow {
	s := a.settings
	origin := a.eff.Origin

	providerVal := s.Provider
	if providerVal == "" {
		providerVal = "—"
	}
	modelVal := s.Model
	if modelVal == "" {
		modelVal = "—"
	}
	effortVal := s.Effort
	if effortVal == "" {
		effortVal = "—"
	}
	cavemanVal := "off"
	if s.Caveman != nil && *s.Caveman {
		cavemanVal = "on"
	}
	readOnlyVal := boolLabel(s.ReadOnlyEnabled())
	retentionVal := "28 days"
	if s.SessionRetentionDays != nil {
		retentionVal = fmt.Sprintf("%d days", *s.SessionRetentionDays)
	}
	bannerVal := "on"
	if s.UI != nil && s.UI.Banner != nil {
		bannerVal = boolLabel(*s.UI.Banner)
	}
	colorsVal := boolLabel(s.ColorsEnabled())
	spinnerVal := boolLabel(s.SpinnerEnabled())
	reasoningVal := boolLabel(s.ReasoningVisible())
	toolCallsVal := boolLabel(s.ToolCallsVisible())
	todosVal := boolLabel(s.TodosVisible())
	mouseVal := boolLabel(s.MouseEnabled())
	showNamesVal := boolLabel(s.SessionNamesVisible())
	permsVal := fmt.Sprintf("%d allow · %d ask · %d deny", len(s.Permissions.Allow), len(s.Permissions.Ask), len(s.Permissions.Deny))
	maxAgentsVal := "3"
	if s.Resilience != nil && s.Resilience.MaxAgents != 0 {
		maxAgentsVal = strconv.Itoa(s.Resilience.MaxAgents)
	}
	planExploreVal := boolLabel(s.PlanExploreEnabled())

	return []settingsRow{
		{key: "provider", label: "provider", kind: "text", value: providerVal, src: sourceLabel(origin["provider"])},
		{key: "model", label: "model", kind: "text", value: modelVal, src: sourceLabel(origin["model"])},
		{key: "effort", label: "effort", kind: "choose", opts: []string{"low", "medium", "high"}, value: effortVal, src: sourceLabel(origin["effort"])},
		{key: "caveman", label: "caveman", kind: "toggle", value: cavemanVal, src: sourceLabel(origin["caveman"])},
		{key: "read_only", label: "read-only tools", kind: "toggle", value: readOnlyVal, src: sourceLabel(origin["read_only"])},
		{key: "session_retention_days", label: "session retention", kind: "text", value: retentionVal, src: sourceLabel(origin["session_retention_days"])},
		{key: "banner", label: "banner", kind: "toggle", value: bannerVal, src: sourceLabel(origin["ui"])},
		{key: "colors", label: "colours", kind: "toggle", value: colorsVal, src: sourceLabel(origin["ui"])},
		{key: "spinner", label: "spinner", kind: "toggle", value: spinnerVal, src: sourceLabel(origin["ui"])},
		{key: "show_reasoning", label: "reasoning", kind: "toggle", value: reasoningVal, src: sourceLabel(origin["ui"])},
		{key: "show_tool_calls", label: "tool calls", kind: "toggle", value: toolCallsVal, src: sourceLabel(origin["ui"])},
		{key: "show_todos", label: "todo panel", kind: "toggle", value: todosVal, src: sourceLabel(origin["ui"])},
		{key: "mouse", label: "mouse capture", kind: "toggle", value: mouseVal, src: sourceLabel(origin["ui"])},
		{key: "show_session_names", label: "session names", kind: "toggle", value: showNamesVal, src: sourceLabel(origin["show_session_names"])},
		{key: "max_agents", label: "max agents", kind: "text", value: maxAgentsVal, src: sourceLabel(origin["resilience"])},
		{key: "plan_explore", label: "plan explore", kind: "toggle", value: planExploreVal, src: sourceLabel(origin["resilience"])},
		{key: "permissions", label: "permissions", kind: "submenu", value: permsVal, src: sourceLabel(origin["permissions"])},
	}
}

func sourceLabel(src config.Source) string {
	if src == "" {
		return "default"
	}
	return string(src)
}

func boolLabel(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func (a *App) handleSettingsKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.settingsState.editMode {
		switch m.String() {
		case "esc":
			a.settingsState.editMode = false
			a.settingsState.errorMsg = ""
			a.editor.Masked = false
			a.editor.Reset()
			return a, nil
		case "enter":
			rows := a.settingsRows()
			if a.settingsState.selected >= len(rows) {
				a.settingsState.editMode = false
				return a, nil
			}
			row := rows[a.settingsState.selected]
			err := a.commitTextRow(row, a.editor.Value())
			a.editor.Masked = false
			a.editor.Reset()
			if err != nil {
				a.settingsState.errorMsg = err.Error()
				return a, nil
			}
			a.settingsState.editMode = false
			a.settingsState.errorMsg = ""
			return a, nil
		default:
			cmd := a.editor.Update(m)
			return a, cmd
		}
	}

	switch m.String() {
	case "up", "k":
		if a.settingsState.selected > 0 {
			a.settingsState.selected--
		}
		return a, nil
	case "down", "j":
		if a.settingsState.selected < len(a.settingsRows())-1 {
			a.settingsState.selected++
		}
		return a, nil
	case "esc":
		a.pop()
		return a, nil
	case "s":
		if a.settingsState.scope == config.ScopeGlobal {
			a.settingsState.scope = config.ScopeProject
		} else {
			a.settingsState.scope = config.ScopeGlobal
		}
		return a, nil
	case "x":
		rows := a.settingsRows()
		if a.settingsState.selected < len(rows) {
			row := rows[a.settingsState.selected]
			if err := a.unsetSetting(row.key); err != nil {
				a.settingsState.errorMsg = err.Error()
			} else {
				a.settingsState.errorMsg = ""
			}
		}
		return a, nil
	case " ", "enter":
		rows := a.settingsRows()
		if a.settingsState.selected >= len(rows) {
			return a, nil
		}
		row := rows[a.settingsState.selected]
		switch row.kind {
		case "submenu":
			return a, a.push(viewPermissions)
		case "toggle":
			if err := a.cycleToggle(row.key); err != nil {
				a.settingsState.errorMsg = err.Error()
			} else {
				a.settingsState.errorMsg = ""
			}
			return a, nil
		case "choose":
			if err := a.cycleChoice(row.key, row.opts); err != nil {
				a.settingsState.errorMsg = err.Error()
			} else {
				a.settingsState.errorMsg = ""
			}
			return a, nil
		case "text":
			a.settingsState.editMode = true
			a.settingsState.errorMsg = ""
			a.editor.Masked = false
			a.editor.SetValue(a.rawValue(row.key))
			_ = a.editor.Focus()
			return a, nil
		}
	}

	cmd := a.editor.Update(m)
	return a, cmd
}

func (a *App) rawValue(key string) string {
	switch key {
	case "provider":
		return a.settings.Provider
	case "model":
		return a.settings.Model
	case "session_retention_days":
		if a.settings.SessionRetentionDays != nil {
			return strconv.Itoa(*a.settings.SessionRetentionDays)
		}
		return ""
	case "max_agents":
		if a.settings.Resilience != nil && a.settings.Resilience.MaxAgents != 0 {
			return strconv.Itoa(a.settings.Resilience.MaxAgents)
		}
		return ""
	}
	return ""
}

func (a *App) commitTextRow(row settingsRow, raw string) error {
	val := strings.TrimSpace(raw)
	switch row.key {
	case "provider":
		if val == "" {
			return a.unsetSetting("provider")
		}
		if !a.isProviderName(val) {
			return fmt.Errorf("unknown provider %q", val)
		}
		return a.mutateSetting(func(s *config.Settings) { s.Provider = val })
	case "model":
		return a.mutateSetting(func(s *config.Settings) { s.Model = val })
	case "session_retention_days":
		if val == "" {
			return a.unsetSetting("session_retention_days")
		}
		n, err := strconv.Atoi(val)
		if err != nil || n <= 0 {
			return fmt.Errorf("session retention must be a positive integer")
		}
		return a.mutateSetting(func(s *config.Settings) { s.SessionRetentionDays = &n })
	case "max_agents":
		if val == "" {
			return a.unsetSetting("max_agents")
		}
		n, err := strconv.Atoi(val)
		if err != nil || n <= 0 {
			return fmt.Errorf("max agents must be a positive integer")
		}
		return a.mutateSetting(func(s *config.Settings) {
			if s.Resilience == nil {
				s.Resilience = &config.ResilienceSettings{}
			}
			s.Resilience.MaxAgents = n
		})
	}
	return fmt.Errorf("cannot edit %q", row.key)
}

func (a *App) cycleToggle(key string) error {
	return a.mutateSetting(func(s *config.Settings) {
		switch key {
		case "caveman":
			s.Caveman = nextBool(s.Caveman)
		case "read_only":
			s.ReadOnly = nextBool(s.ReadOnly)
		case "banner":
			if s.UI == nil {
				s.UI = &config.UISettings{}
			}
			s.UI.Banner = nextBool(s.UI.Banner)
		case "colors":
			if s.UI == nil {
				s.UI = &config.UISettings{}
			}
			s.UI.Colors = nextBool(s.UI.Colors)
		case "spinner":
			if s.UI == nil {
				s.UI = &config.UISettings{}
			}
			s.UI.Spinner = nextBool(s.UI.Spinner)
		case "show_reasoning":
			if s.UI == nil {
				s.UI = &config.UISettings{}
			}
			s.UI.ShowReasoning = nextBool(s.UI.ShowReasoning)
		case "show_tool_calls":
			if s.UI == nil {
				s.UI = &config.UISettings{}
			}
			s.UI.ShowToolCalls = nextBool(s.UI.ShowToolCalls)
		case "show_todos":
			if s.UI == nil {
				s.UI = &config.UISettings{}
			}
			s.UI.ShowTodos = nextBool(s.UI.ShowTodos)
		case "mouse":
			if s.UI == nil {
				s.UI = &config.UISettings{}
			}
			s.UI.Mouse = nextBool(s.UI.Mouse)
		case "show_session_names":
			s.ShowSessionNames = nextBool(s.ShowSessionNames)
		case "plan_explore":
			if s.Resilience == nil {
				s.Resilience = &config.ResilienceSettings{}
			}
			s.Resilience.PlanExplore = nextBool(s.Resilience.PlanExplore)
		}
	})
}

func (a *App) cycleChoice(key string, opts []string) error {
	return a.mutateSetting(func(s *config.Settings) {
		if key != "effort" {
			return
		}
		cur := s.Effort
		idx := -1
		for i, o := range opts {
			if o == cur {
				idx = i
				break
			}
		}
		s.Effort = opts[(idx+1)%len(opts)]
	})
}

func (a *App) unsetSetting(key string) error {
	return a.mutateSetting(func(s *config.Settings) {
		switch key {
		case "provider":
			s.Provider = ""
		case "model":
			s.Model = ""
		case "effort":
			s.Effort = ""
		case "caveman":
			s.Caveman = nil
		case "read_only":
			s.ReadOnly = nil
		case "session_retention_days":
			s.SessionRetentionDays = nil
		case "banner":
			if s.UI != nil {
				s.UI.Banner = nil
			}
		case "colors":
			if s.UI != nil {
				s.UI.Colors = nil
			}
		case "spinner":
			if s.UI != nil {
				s.UI.Spinner = nil
			}
		case "show_reasoning":
			if s.UI != nil {
				s.UI.ShowReasoning = nil
			}
		case "show_tool_calls":
			if s.UI != nil {
				s.UI.ShowToolCalls = nil
			}
		case "show_todos":
			if s.UI != nil {
				s.UI.ShowTodos = nil
			}
		case "mouse":
			if s.UI != nil {
				s.UI.Mouse = nil
			}
		case "show_session_names":
			s.ShowSessionNames = nil
		case "plan_explore":
			if s.Resilience != nil {
				s.Resilience.PlanExplore = nil
			}
		case "max_agents":
			if s.Resilience != nil {
				s.Resilience.MaxAgents = 0
			}
		}
	})
}

func (a *App) mutateSetting(fn func(*config.Settings)) error {
	if err := config.Mutate(a.settingsState.scope, a.workdir, func(s *config.Settings) error {
		fn(s)
		return nil
	}); err != nil {
		return err
	}
	return a.reloadSettings()
}

func nextBool(b *bool) *bool {
	if b == nil {
		t := true
		return &t
	}
	if *b {
		f := false
		return &f
	}
	return nil
}

func (a *App) isProviderName(name string) bool {
	for _, n := range a.providerNames() {
		if n == name {
			return true
		}
	}
	return false
}
