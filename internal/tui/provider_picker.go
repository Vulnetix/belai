package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/tui/components"
)

// The provider picker replaces cycling on a role's provider row. Cycling
// derived "next" from the committed provider, and a provider is only
// committed once its test passes, so one failing provider pinned the cycle:
// every press restarted from the same place and the providers after it were
// unreachable. The picker keeps its own cursor, so a test result only
// labels a provider and never decides whether it can be chosen.

// providerTestLabel is the last test outcome recorded for a provider.
const (
	providerPassed   = "passed"
	providerFailed   = "failed"
	providerUntested = "untested"
)

// recordProviderTests remembers the outcome of a finished test against every
// provider it probed. It only labels the picker; it never gates a choice.
func (a *App) recordProviderTests(chatKeys []string, passed bool) {
	if a.providerTests == nil {
		a.providerTests = map[string]string{}
	}
	status := providerFailed
	if passed {
		status = providerPassed
	}
	for _, k := range chatKeys {
		if name, _, ok := strings.Cut(k, "\x00"); ok && name != "" {
			a.providerTests[name] = status
		}
	}
}

// providerTestStatus returns the label the picker shows beside a provider.
func (a *App) providerTestStatus(name string) string {
	if s, ok := a.providerTests[name]; ok {
		return s
	}
	return providerUntested
}

// openProviderPicker lists opts for a role, with the cursor on the provider
// the role uses now. An empty entry means "inherit the main model".
func (a *App) openProviderPicker(role modelRole, opts []string, current string) tea.Cmd {
	if len(opts) == 0 {
		return nil
	}
	a.modelState.pickingProvider = true
	a.modelState.providerRole = role
	a.modelState.providerOpts = opts
	a.modelState.providerIdx = max(indexOfString(opts, current), 0)
	return nil
}

func (a *App) handleProviderPickerKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	opts := a.modelState.providerOpts
	switch m.String() {
	case "esc":
		a.modelState.pickingProvider = false
	case "up", "k":
		if a.modelState.providerIdx > 0 {
			a.modelState.providerIdx--
		} else {
			a.modelState.providerIdx = len(opts) - 1
		}
	case "down", "j":
		if a.modelState.providerIdx < len(opts)-1 {
			a.modelState.providerIdx++
		} else {
			a.modelState.providerIdx = 0
		}
	case "t":
		return a, a.retestProvider(opts[clampIdx(a.modelState.providerIdx, len(opts))])
	case " ", "enter":
		a.modelState.pickingProvider = false
		return a, a.chooseProvider(a.modelState.providerRole, opts[clampIdx(a.modelState.providerIdx, len(opts))])
	}
	return a, nil
}

// chooseProvider applies a provider chosen in the picker. The model picker
// opens on it next; the pick is tested and saved together with the provider,
// and a failed test leaves the picker free to choose again.
func (a *App) chooseProvider(role modelRole, name string) tea.Cmd {
	switch role {
	case roleAgent:
		a.setPendingProvider(roleAgent, name)
		return a.openAgentModelPicker()
	case roleFast:
		a.setPendingProvider(roleFast, name)
		return a.openFastModelPicker()
	}
	return a.selectClassifierProvider(name)
}

// retestProvider re-runs the test for the provider under the cursor without
// changing the selection.
func (a *App) retestProvider(name string) tea.Cmd {
	if name == "" {
		return nil
	}
	switch a.modelState.providerRole {
	case roleAgent:
		return a.stageAgent("provider", name, "", func(*config.Settings) {}, func() {})
	}
	return nil
}

// providerPicker renders the provider list with each provider's test label.
func (a *App) providerPicker() string {
	var b strings.Builder
	b.WriteString(components.MutedStyle.Render("provider for ") + components.Chip(string(a.modelState.providerRole), components.ColorTeal) + "\n")
	b.WriteString(components.MutedStyle.Render("Test results only label a provider. Every provider stays selectable.") + "\n\n")
	for i, name := range a.modelState.providerOpts {
		selected := i == a.modelState.providerIdx
		shown := name
		if shown == "" {
			shown = "(main model)"
		}
		label := a.providerTestStatus(name)
		glyph, style := "○", components.MutedStyle
		switch label {
		case providerPassed:
			glyph, style = "●", components.AccentStyle
		case providerFailed:
			glyph, style = "▲", components.WarnStyle
		}
		if name == "" {
			glyph, label, style = " ", "", components.MutedStyle
		}
		line := style.Render(glyph) + " "
		if selected {
			line += components.EmphStyle.Render(padRight(shown, 24))
		} else {
			line += components.MutedStyle.Render(padRight(shown, 24))
		}
		b.WriteString(components.Cursor(selected) + line + style.Render(label) + "\n")
	}
	b.WriteString("\n" + components.HelpBar("↑↓", "move", "⏎", "select", "t", "retest", "esc", "cancel") + "\n")
	return b.String()
}
