package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/belai/internal/commands"
	"github.com/vulnetix/belai/internal/tui/components"
)

// runsOutputState tracks the full-screen output reader for a single run or
// activity. It is modelled on planReviewState but only needs the activity id
// and a viewport. content, when non-empty, overrides the activity registry and
// is used for logs that are not tied to a live activity row.
type runsOutputState struct {
	id      string
	content string
	vp      viewport.Model
	// shown is the text last set on the viewport, for snapshot links.
	shown string
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
	output := s.content
	if output == "" && a.activity != nil && s.id != "" {
		output = a.activity.Output(s.id)
	}
	// Process activity rows may open before any progress event has been
	// promoted. Fall back to the process log file on disk.
	if output == "" && strings.HasPrefix(s.id, "proc-") && a.procManager != nil {
		if tail, err := a.procManager.TailByID(strings.TrimPrefix(s.id, "proc-"), 256); err == nil {
			output = tail
		}
	}
	if output == "" {
		output = "(no output)"
	}
	s.shown = output
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
	pairs := []string{"pgup/pgdn", "page", "shift+↑↓", "line", "ctrl+home/end", "top/bottom"}
	if a.runsOutputSnapshot() != "" {
		pairs = append(pairs, "ctrl+y", "open snapshot")
	}
	pairs = append(pairs, "esc", "back")
	b.WriteString(components.HelpBar(pairs...) + "\n")
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
	case "ctrl+y":
		return a, openLink(a.runsOutputSnapshot())
	}
	return a, nil
}

// runsOutputSnapshot is the snapshot link ctrl+y opens from a scanner's
// output: the first one on screen, else the last one printed.
func (a *App) runsOutputSnapshot() string {
	snaps := commands.ParseSnapshots(a.runsOutput.shown)
	if len(snaps) == 0 {
		return ""
	}
	lines := strings.Split(a.runsOutput.shown, "\n")
	top := a.runsOutput.vp.YOffset
	bottom := min(top+a.runsOutput.vp.Height, len(lines))
	if top < bottom {
		visible := strings.Join(lines[top:bottom], "\n")
		for _, s := range snaps {
			if strings.Contains(visible, s.URL) {
				return s.URL
			}
		}
	}
	return snaps[len(snaps)-1].URL
}
