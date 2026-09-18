package tui

import (
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/tui/components"
)

// renderFrame renders a.messages into a.lastFrame the way chatView does, so
// the hover hit-tests exercise the same provenance the real frame carries.
func renderFrame(t *testing.T, a *App) {
	t.Helper()
	body, lm := components.MessageList{
		Messages:  a.messages,
		Width:     a.contentWidth(),
		ExpandAll: a.expandAll,
		ShowTools: true,
	}.Render()
	a.lastBody = body
	a.lastFrame = frame{
		lines:   lm,
		top:     a.headerHeight(),
		left:    a.contentLeft(),
		width:   a.contentWidth(),
		height:  24,
		yOffset: 0,
	}
}

// hoverLine finds the first selectable line with the given owner and collapsed
// flag and returns its content-line index.
func hoverLine(t *testing.T, a *App, owner int, collapsed bool) int {
	t.Helper()
	for i, l := range a.lastFrame.lines {
		if l.Owner == owner && l.Collapsed == collapsed && !l.Chrome && l.Width > 0 {
			return i
		}
	}
	t.Fatalf("no line with owner %d collapsed=%v in %+v", owner, collapsed, a.lastFrame.lines)
	return 0
}

// pointAt sets the recorded pointer over the middle of the given content line.
func pointAt(a *App, line int) {
	l := a.lastFrame.lines[line]
	a.mouseX = a.lastFrame.left + l.Col + l.Width/2
	a.mouseY = a.lastFrame.top + line
	a.mousePresent = true
}

func TestRecomputeHoverCollapsedPanel(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.width = 120
	a.height = 40
	a.messages = []components.Message{
		{Role: "assistant", Content: "l1\nl2\nl3\nl4\nl5"},
	}
	renderFrame(t, a)
	pointAt(a, hoverLine(t, a, 0, true))
	a.recomputeHover()

	if !a.hover.collapsed || a.hover.msg != 0 {
		t.Fatalf("hover = %+v, want collapsed panel 0", a.hover)
	}
	if hint := a.hoverHint(); !strings.Contains(hint, "ctrl+o") || !strings.Contains(hint, "expand all") {
		t.Fatalf("hint = %q, want ctrl+o expand all", hint)
	}
}

func TestRecomputeHoverNone(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.width = 120
	a.height = 40
	a.messages = []components.Message{{Role: "system", Content: "done"}}
	renderFrame(t, a)
	pointAt(a, hoverLine(t, a, 0, false))
	a.recomputeHover()

	if a.hover != (hoverTarget{}) {
		t.Fatalf("hover = %+v, want none", a.hover)
	}
	if a.hoverHint() != "" {
		t.Fatalf("hint = %q, want empty", a.hoverHint())
	}
}

func TestRecomputeHoverExpandedPanelShowsNoHint(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.width = 120
	a.height = 40
	a.messages = []components.Message{
		{Role: "assistant", Content: "l1\nl2\nl3\nl4\nl5"},
	}
	a.expandAll = true
	renderFrame(t, a)
	pointAt(a, hoverLine(t, a, 0, false))
	a.recomputeHover()

	if a.hover != (hoverTarget{}) {
		t.Fatalf("hover = %+v, want none after expand", a.hover)
	}
	if a.hoverHint() != "" {
		t.Fatalf("hint = %q, want empty after expand", a.hoverHint())
	}
}
