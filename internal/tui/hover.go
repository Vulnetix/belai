package tui

import (
	"github.com/vulnetix/signet/internal/tui/components"
)

// hoverTarget records what the pointer is over. It is derived every frame
// from the last mouse position and the provenance the renderers attach to
// each transcript line, so it never goes stale the way a one-shot hit-test
// would: expanding with ctrl+o or streaming a delta re-derives the target
// without another mouse event.
type hoverTarget struct {
	collapsed bool // a truncated panel — ctrl+o offered
	msg       int  // message index for the target
}

// recomputeHover re-derives a.hover from the last mouse position and the
// current rendered frame. It is called from chatView after lastFrame is
// built, so the hint always matches the frame on screen.
func (a *App) recomputeHover() {
	a.hover = hoverTarget{}
	if !a.mousePresent || a.view != viewChat {
		return
	}
	p, ok := contentPos(a.mouseX, a.mouseY, a.lastFrame)
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

// hoverHint renders the footer hint for the current hover target: the actions
// available and the keybinding for each. It is empty when nothing actionable
// is under the pointer.
func (a *App) hoverHint() string {
	if a.hover.collapsed {
		return components.HelpBar("ctrl+o", "expand all")
	}
	return ""
}
