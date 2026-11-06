package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/belai/internal/tui/components"
)

// relayChrome is every non-body row of the review: padding, header, the size
// line, the error line and the help bar.
const relayChrome = 9

func (a *App) relayWidth() int {
	w := a.contentWidth() - 2
	if w < 20 {
		w = 20
	}
	return w
}

func (a *App) relayHeight() int {
	h := a.height - relayChrome
	if h < 5 {
		h = 5
	}
	return h
}

// setContent loads the draft into the review viewport.
func (s *relayState) setContent(w, h int) {
	s.vp = viewport.New(w, h)
	s.vp.SetContent(lipgloss.NewStyle().Width(w).Render(s.draft))
	s.vp.GotoTop()
}

func (a *App) enterRelay() tea.Cmd {
	a.relay.setContent(a.relayWidth(), a.relayHeight())
	return nil
}

func (a *App) relayView() string {
	w := a.relayWidth()
	var b strings.Builder
	b.WriteString(components.SectionHeader("Review handoff", "esc discard", w))
	b.WriteString("\n")
	size := fmt.Sprintf("%d of %d bytes", len(a.relay.draft), a.settings.HandoffMaxBytes())
	if a.relay.edited {
		size += "  ·  (edited by you)"
	}
	b.WriteString(components.MutedStyle.Render("a fresh session starts from this note; this session stays as it is  ·  " + size))
	b.WriteString("\n")

	a.relay.vp.Width = w
	a.relay.vp.Height = a.relayHeight()
	b.WriteString(a.relay.vp.View())
	b.WriteString("\n")
	switch {
	case a.relay.drafting:
		b.WriteString(components.MutedStyle.Render("working…") + "\n")
	case a.relay.err != "":
		b.WriteString(components.WarnStyle.Render(oneLine(a.relay.err, w)) + "\n")
	default:
		b.WriteString("\n")
	}
	b.WriteString(components.HelpBar("enter", "accept", "e", "edit", "r", "redraft", "pgup/pgdn", "page", "esc", "discard") + "\n")
	return lipgloss.NewStyle().Padding(1).Render(b.String())
}

func (a *App) handleRelayKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.String() {
	case "esc":
		return a, a.discardRelay()
	case "enter":
		return a, a.acceptRelay()
	case "e":
		return a, a.openRelayEditor()
	case "r":
		return a, a.redraftRelay()
	case "pgup":
		a.relay.vp.PageUp()
	case "pgdown":
		a.relay.vp.PageDown()
	case "up", "k", "shift+up":
		a.relay.vp.ScrollUp(1)
	case "down", "j", "shift+down":
		a.relay.vp.ScrollDown(1)
	case "ctrl+home":
		a.relay.vp.GotoTop()
	case "ctrl+end":
		a.relay.vp.GotoBottom()
	}
	return a, nil
}
