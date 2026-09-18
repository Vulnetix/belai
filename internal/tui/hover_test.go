package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

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

func TestRecomputeHoverSession(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.width = 120
	a.height = 40
	a.sessionID = "abcd1234abcd1234"
	a.mode = "agent"
	a.refreshFooter()
	col, w, ok := a.footer.SessionSpan()
	if !ok {
		t.Fatal("expected a session span")
	}
	row := a.height - 1 - a.footerHeight() + 2
	a.mousePresent = true
	a.mouseX = a.contentLeft() + col + w/2
	a.mouseY = row
	a.recomputeHover()

	if !a.hover.session {
		t.Fatalf("hover = %+v, want session", a.hover)
	}
	if hint := a.hoverHint(); !strings.Contains(hint, "ctrl+x") || !strings.Contains(hint, "copy session id") {
		t.Fatalf("hint = %q, want ctrl+x copy session id", hint)
	}
}

func TestHitSessionGeometry(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.width = 120
	a.height = 40
	a.sessionID = "abcd1234abcd1234"
	a.mode = "agent"
	a.refreshFooter()

	col, w, ok := a.footer.SessionSpan()
	if !ok {
		t.Fatal("expected a session span")
	}
	row := a.height - 1 - a.footerHeight() + 2
	if !a.hitSession(a.contentLeft()+col+1, row) {
		t.Fatalf("hitSession at (%d,%d) should hit", a.contentLeft()+col+1, row)
	}
	if !a.hitSession(a.contentLeft()+col+w-1, row) {
		t.Fatalf("hitSession at the session's last cell should hit")
	}
	if a.hitSession(0, row) {
		t.Fatalf("hitSession far left should miss")
	}
	if a.hitSession(a.contentLeft()+col, a.height-1) {
		t.Fatalf("hitSession outside the footer should miss")
	}
}

func TestCtrlXCopiesSessionID(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.sessionID = "abcd1234abcd1234"
	cmd := a.handleChatKey(tea.KeyMsg{Type: tea.KeyCtrlX})
	if cmd == nil {
		t.Fatal("ctrl+x should copy the session id")
	}
	msg := cmd()
	copied, ok := msg.(copiedMsg)
	if !ok {
		t.Fatalf("ctrl+x produced %#v, want copiedMsg", msg)
	}
	if !strings.Contains(copied.text, "copied session id to clipboard") {
		t.Fatalf("copy feedback = %q, want session id copy", copied.text)
	}
}
