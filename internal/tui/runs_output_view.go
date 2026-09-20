package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/signet/internal/tui/components"
)

// runsOutputState tracks the full-screen output reader for a single run or
// activity. It is modelled on planReviewState but only needs the activity id
// and a viewport.
type runsOutputState struct {
	id string
	vp viewport.Model
}

// runsOutputChrome is every non-body row in the output view: padding, header,
// help bar, and rule.
const runsOutputChrome = 7

func (a *App) enterRunsOutput() tea.Cmd {
	w := a.contentWidth() - 2
	if w < 1 {
		w = 1
	}
	h := a.height - runsOutputChrome
	if h < 5 {
		h = 5
	}
	a.runsOutput.vp = viewport.New(w, h)
	a.runsOutput.setContent(a)
	return nil
}

func (s *runsOutputState) setContent(a *App) {
	output := ""
	if a.activity != nil && s.id != "" {
		output = a.activity.Output(s.id)
	}
	if output == "" {
		output = "(no output)"
	}
	s.vp.SetContent(output)
	s.vp.GotoTop()
}

// runsOutputWidth returns the usable interior width of the output view.
func (a *App) runsOutputWidth() int {
	w := a.contentWidth() - 2
	if w < 1 {
		w = 1
	}
	return w
}

// runsOutputHeight returns the number of rows available for the output body.
func (a *App) runsOutputHeight() int {
	h := a.height - runsOutputChrome
	if h < 5 {
		h = 5
	}
	return h
}

func (a *App) runsOutputView() string {
	w := a.runsOutputWidth()
	var b strings.Builder

	label := a.runsOutput.id
	if act := a.findActivity(label); act.Label != "" {
		label = act.Label
	}
	subtitle := "esc back"
	b.WriteString(components.SectionHeader("activity output", subtitle, w))
	b.WriteString(components.MutedStyle.Render("  "+label) + "\n")

	a.runsOutput.vp.Width = w
	a.runsOutput.vp.Height = a.runsOutputHeight()
	a.runsOutput.setContent(a)
	b.WriteString(a.runsOutput.vp.View())
	b.WriteString("\n")
	b.WriteString(components.HelpBar("pgup/pgdn", "page", "shift+↑↓", "line", "ctrl+home/end", "top/bottom", "esc", "back") + "\n")
	return lipgloss.NewStyle().Padding(1).Render(b.String())
}

func (a *App) handleRunsOutputKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.String() {
	case "esc":
		a.pop()
		return a, nil
	case "pgup":
		a.runsOutput.vp.PageUp()
		return a, nil
	case "pgdown":
		a.runsOutput.vp.PageDown()
		return a, nil
	case "shift+up":
		a.runsOutput.vp.ScrollUp(1)
		return a, nil
	case "shift+down":
		a.runsOutput.vp.ScrollDown(1)
		return a, nil
	case "ctrl+home":
		a.runsOutput.vp.GotoTop()
		return a, nil
	case "ctrl+end":
		a.runsOutput.vp.GotoBottom()
		return a, nil
	}
	return a, nil
}
