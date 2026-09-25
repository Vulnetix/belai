package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/signet/internal/budget"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/tui/components"
)

// The screen switcher puts every full-screen view one letter away. f1 opens
// it from chat or from any screen; a letter jumps, and a screen that is
// already open further down the stack is returned to rather than opened twice,
// so esc always walks back through distinct screens.

// screenEntry is one destination in the switcher.
type screenEntry struct {
	key    string
	name   string
	desc   string
	view   viewState
	open   func(*App) tea.Cmd
	status func(*App) string
}

func viaCommand(line string) func(*App) tea.Cmd {
	return func(a *App) tea.Cmd { return a.handleCommand(line) }
}

var screenEntries = []screenEntry{
	{key: "a", name: "agents", desc: "running agents, profiles and the audit trail", view: viewAgent, open: viaCommand("/agents"), status: (*App).agentsStatus},
	{key: "m", name: "model", desc: "provider and model for each role", view: viewModel, open: viaCommand("/model"), status: func(a *App) string { return run.WireModel(a.cfg.Provider, a.cfg.Model) }},
	{key: "p", name: "providers", desc: "credentials and local models", view: viewProviders, open: viaCommand("/providers"), status: func(a *App) string { return a.providerDisplayLabel(a.cfg.Provider) }},
	{key: "s", name: "settings", desc: "every setting, by scope", view: viewSettings, open: viaCommand("/settings")},
	{key: "b", name: "budgets", desc: "token budgets per provider and model", view: viewBudgets, open: viaCommand("/budgets"), status: (*App).budgetsStatus},
	{key: "k", name: "permissions", desc: "allow, ask and deny rules", view: viewPermissions, open: viaCommand("/permissions"), status: (*App).permissionsStatus},
	{key: "r", name: "prompts", desc: "the prompt library", view: viewPrompts, open: viaCommand("/prompts")},
	{key: "x", name: "processes", desc: "the process library", view: viewProcesses, open: viaCommand("/processes")},
	{key: "l", name: "lsp", desc: "language-server diagnostics", view: viewLSP, open: viaCommand("/lsp")},
	{key: "v", name: "vulnetix", desc: "scanners and the AI Firewall", view: viewVulnetixConfig, open: func(a *App) tea.Cmd { return a.push(viewVulnetixConfig) }, status: func(a *App) string { return "firewall " + onOffLabel(a.firewallEnabled()) }},
	{key: "h", name: "sessions", desc: "resume an earlier session", view: viewResume, open: viaCommand("/resume")},
}

func onOffLabel(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// agentsStatus summarises the ledger for the switcher row.
func (a *App) agentsStatus() string {
	live, parked := a.agentCounts()
	var parts []string
	if live > 0 {
		parts = append(parts, fmt.Sprintf("%d running", live))
	}
	if parked > 0 {
		parts = append(parts, fmt.Sprintf("%d parked", parked))
	}
	return strings.Join(parts, " · ")
}

func (a *App) permissionsStatus() string {
	return "guardrails " + onOffLabel(a.guardrailsEnabled()) + " · ask " + onOffLabel(a.askEnabled())
}

// screensBlocked reports whether the current view must be answered before the
// user can leave it: an approval, a question, or a plan waiting on a verdict.
func (a *App) screensBlocked() bool {
	switch a.view {
	case viewPermissionAsk, viewClarify, viewPlanReview, viewResumeCompact:
		return true
	case viewChat:
		return a.savePromptMode || a.saveFileMode || a.promptAction || a.historyActive || a.forgeFlowActive()
	case viewScreens:
		return false
	}
	// An inline field edit owns the shared editor; leaving would drop it.
	return a.editor.Value() != "" || a.editor.Masked
}

// toggleScreens opens or closes the switcher.
func (a *App) toggleScreens() tea.Cmd {
	if a.view == viewScreens {
		a.pop()
		return nil
	}
	if a.screensBlocked() {
		return nil
	}
	if a.view == viewChat {
		a.chatDraft = a.editor.Value()
	}
	return a.push(viewScreens)
}

// enterScreens highlights the screen the user came from, if it is listed.
func (a *App) enterScreens() tea.Cmd {
	a.screensSel = 0
	if n := len(a.viewStack); n >= 2 {
		below := a.viewStack[n-2]
		for i, e := range screenEntries {
			if e.view == below {
				a.screensSel = i
			}
		}
	}
	return nil
}

// jumpToScreen replaces the switcher with the chosen screen.
func (a *App) jumpToScreen(e screenEntry) tea.Cmd {
	if n := len(a.viewStack); n > 0 && a.viewStack[n-1] == viewScreens {
		a.viewStack = a.viewStack[:n-1]
	}
	for i, v := range a.viewStack {
		if v == e.view {
			a.viewStack = a.viewStack[:i]
			break
		}
	}
	if n := len(a.viewStack); n > 0 {
		a.view = a.viewStack[n-1]
	} else {
		a.view = viewChat
	}
	cmd := e.open(a)
	if a.view == viewChat {
		a.restoreChatDraft()
	}
	return cmd
}

func (a *App) handleScreensKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.String() {
	case "esc":
		a.pop()
		return a, nil
	case "up":
		if a.screensSel > 0 {
			a.screensSel--
		}
		return a, nil
	case "down":
		if a.screensSel < len(screenEntries)-1 {
			a.screensSel++
		}
		return a, nil
	case "enter":
		return a, a.jumpToScreen(screenEntries[a.screensSel])
	}
	// Every letter is a destination, so the arrows alone move the highlight.
	for _, e := range screenEntries {
		if m.String() == e.key {
			return a, a.jumpToScreen(e)
		}
	}
	return a, nil
}

func (a *App) screensView() string {
	w := a.contentWidth()
	var b strings.Builder
	b.WriteString(components.SectionHeader("Screens", "f1 closes", w))
	for i, e := range screenEntries {
		selected := i == a.screensSel
		name := fmt.Sprintf("%-13s", e.name)
		if selected {
			name = components.EmphStyle.Render(name)
		}
		left := components.Cursor(selected) + components.KeyStyle.Render(e.key) + "  " + name + components.MutedStyle.Render(e.desc)
		line := left
		if e.status != nil {
			if st := e.status(a); st != "" {
				pad := w - lipgloss.Width(left) - lipgloss.Width(st) - 2
				if pad >= 2 {
					line = left + strings.Repeat(" ", pad) + components.AccentStyle.Render(st)
				}
			}
		}
		b.WriteString(ansi.Truncate(line, w, "…") + "\n")
	}
	b.WriteString("\n" + components.HelpBar("letter", "open", "↑↓", "move", "⏎", "open", "esc", "back") + "\n")
	return lipgloss.NewStyle().Padding(1).Render(b.String())
}

// budgetsStatus summarises the selected model's budgets for the switcher row:
// how many it has and the worst state among them.
func (a *App) budgetsStatus() string {
	bs := a.settings.BudgetsFor(a.cfg.Provider, a.cfg.Model)
	if len(bs) == 0 || a.budgets == nil {
		return ""
	}
	gs := a.budgets.Gauges(bs)
	return fmt.Sprintf("%d for this model · %s", len(gs), budget.Worst(gs))
}
