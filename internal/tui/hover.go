package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/clipboard"
	"github.com/vulnetix/signet/internal/tui/components"
)

// hoverTarget records what the pointer is over. It is derived every frame
// from the last mouse position and the provenance the renderers attach to
// each transcript line, so it never goes stale the way a one-shot hit-test
// would: expanding with ctrl+o or streaming a delta re-derives the target
// without another mouse event.
type hoverTarget struct {
	collapsed bool // a truncated panel — ctrl+o offered
	session   bool // the footer's session segment — ctrl+x offered
	msg       int  // message index for the target
}

// recomputeHover re-derives a.hover from the last mouse position, the current
// rendered frame and the footer geometry. It is called from chatView after
// lastFrame is built, so the hint always matches the frame on screen.
func (a *App) recomputeHover() {
	a.hover = hoverTarget{}
	if !a.mousePresent || a.view != viewChat {
		return
	}
	x, y := a.mouseX, a.mouseY
	if a.hitSession(x, y) {
		a.hover.session = true
		return
	}
	p, ok := contentPos(x, y, a.lastFrame)
	if !ok || p.Line < 0 || p.Line >= len(a.lastFrame.lines) {
		return
	}
	line := a.lastFrame.lines[p.Line]
	if line.Owner < 0 {
		return
	}
	a.hover.msg = line.Owner
	a.hover.collapsed = line.Collapsed
}

// hitSession reports whether a screen cell lies over the footer's session
// segment. The segment lives on the footer's second content line (rule, line
// 1, line 2), right-aligned, at the column span SessionSpan returns.
func (a *App) hitSession(x, y int) bool {
	h := a.footerHeight()
	top := a.height - 1 - h
	if y < top || y >= top+h {
		return false
	}
	if y-top != 2 {
		return false
	}
	col, width, ok := a.footer.SessionSpan()
	if !ok {
		return false
	}
	left := a.contentLeft()
	return x >= left+col && x < left+col+width
}

// hoverHint renders the footer hint for the current hover target: the actions
// available and the keybinding for each. It is empty when nothing actionable
// is under the pointer.
func (a *App) hoverHint() string {
	var pairs []string
	if a.hover.collapsed {
		pairs = append(pairs, "ctrl+o", "expand all")
	}
	if a.hover.session {
		pairs = append(pairs, "ctrl+x", "copy session id")
	}
	if len(pairs) == 0 {
		return ""
	}
	return components.HelpBar(pairs...)
}

// copySessionID puts the full session id on the clipboard.
func (a *App) copySessionID() tea.Cmd {
	if a.sessionID == "" {
		return nil
	}
	id := a.sessionID
	return func() tea.Msg {
		method, err := clipboard.Copy(id)
		if err != nil {
			return copiedMsg{text: "copy failed: " + err.Error()}
		}
		return copiedMsg{text: "copied session id to clipboard (" + method + ")"}
	}
}
