package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/tui/components"
)

// frameFor builds a frame the way chatView does, for a viewport at the top.
func frameFor(lines components.LineMap, top, left, width, height int) frame {
	return frame{lines: lines, top: top, left: left, width: width, height: height}
}

func TestContentPos(t *testing.T) {
	f := frameFor(components.LineMap{{}, {}}, 1, 1, 40, 10)

	// Inside the viewport rect.
	p, ok := contentPos(2, 2, f)
	if !ok || p != (components.Pos{Line: 1, Col: 1}) {
		t.Fatalf("inside: got (%v, %v), want (Pos{1 1}, true)", p, ok)
	}
	// Each of the four outside cases.
	outside := []struct {
		name string
		x, y int
	}{
		{"left", 0, 2},
		{"above", 2, 0},
		{"right", 41, 2},
		{"below", 2, 11},
	}
	for _, c := range outside {
		if _, ok := contentPos(c.x, c.y, f); ok {
			t.Fatalf("%s: (%d, %d) should be outside", c.name, c.x, c.y)
		}
	}
}

func TestContentPosFollowsScrollOffset(t *testing.T) {
	f := frameFor(components.LineMap{{}}, 1, 1, 40, 10)
	f.yOffset = 40
	p, ok := contentPos(2, 1, f)
	if !ok || p.Line != 40 {
		t.Fatalf("scrolled: got (%v, %v), want line 40", p, ok)
	}
}

func TestClampPos(t *testing.T) {
	f := frameFor(make(components.LineMap, 10), 1, 1, 40, 10)

	// Top-left corner clamps to the origin.
	if p := clampPos(-5, -5, f); p != (components.Pos{Line: 0, Col: 0}) {
		t.Fatalf("top-left: got %v", p)
	}
	// Far off the bottom-right: line clamps to the last content line, the
	// column is left unclamped (per-line clamping in LineMap handles it).
	p := clampPos(999, 999, f)
	if p.Line != 9 {
		t.Fatalf("bottom-right: line = %d, want 9", p.Line)
	}
	if p.Col < 0 {
		t.Fatalf("bottom-right: col = %d, want >= 0", p.Col)
	}
	// A parked pointer just below the frame clamps to the last line.
	p = clampPos(5, 12, f)
	if p.Line != 9 || p.Col != 4 {
		t.Fatalf("below: got %v, want {9 4}", p)
	}
}

func TestChromeHeightDecomposition(t *testing.T) {
	// The step-1 geometry refactor is value-identical: header plus below
	// always equals the old folded total, across banner/autocomplete/
	// attachment permutations.
	cases := []struct {
		name       string
		messages   int // >=3 hides the banner
		autocomple bool
		attach     bool
	}{
		{"bare", 0, false, false},
		{"banner", 1, false, false},
		{"no banner", 3, false, false},
		{"autocomplete", 1, true, false},
		{"attachment", 1, false, true},
		{"everything", 3, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := NewApp(t.TempDir(), "")
			a.width, a.height = 80, 24
			for i := 0; i < c.messages; i++ {
				a.messages = append(a.messages, components.Message{Role: "user", Content: "msg"})
			}
			if c.autocomple {
				a.autocomplete = []string{"x"}
			}
			if c.attach {
				a.attachSeq = 1
				a.attachments[1] = &attachment{id: 1, text: "@f", state: attachSafe}
				a.attachOrder = []int{1}
			}
			a.relayout()
			if got, want := a.headerHeight()+a.belowViewportHeight(), a.chromeHeight(); got != want {
				t.Fatalf("decomposition: %d != %d", got, want)
			}
			wantHeader := 1
			if a.bannerVisible() {
				wantHeader += a.bannerHeight()
			}
			if a.headerHeight() != wantHeader {
				t.Fatalf("headerHeight = %d, want %d", a.headerHeight(), wantHeader)
			}
		})
	}
}

// seededChatApp returns an app whose lastFrame is populated with a known
// transcript, and the line index of the line containing needle.
func seededChatApp(t *testing.T, needle string) (*App, int) {
	t.Helper()
	a := NewApp(t.TempDir(), "")
	a.width, a.height = 80, 24
	a.messages = []components.Message{
		{Role: "user", Content: "hello selection world"},
		{Role: "assistant", Content: "acknowledged"},
	}
	a.View() // populates lastFrame

	ml := components.MessageList{Messages: a.messages, Width: a.contentWidth()}
	_, lm := ml.Render()
	idx := -1
	for i, sl := range lm {
		if !sl.Chrome && strings.Contains(sl.Text, needle) {
			idx = i
			break
		}
	}
	if idx == -1 {
		t.Fatalf("needle %q not found in rendered map", needle)
	}
	return a, idx
}

func TestMouseDragSelectsAndCopies(t *testing.T) {
	a, idx := seededChatApp(t, "hello selection world")
	ml := components.MessageList{Messages: a.messages, Width: a.contentWidth()}
	_, lm := ml.Render()
	sl := lm[idx]

	x := a.lastFrame.left + sl.Col + 2
	y := a.lastFrame.top + (idx - a.lastFrame.yOffset)

	// Press.
	_, cmd := a.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: y})
	if !a.sel.dragging || !a.sel.active {
		t.Fatalf("after press: dragging=%v active=%v", a.sel.dragging, a.sel.active)
	}
	if cmd != nil {
		t.Fatalf("press must not return a copy cmd")
	}
	if a.sel.anchor != (components.Pos{Line: idx, Col: sl.Col + 2}) {
		t.Fatalf("anchor = %v, want line %d col %d", a.sel.anchor, idx, sl.Col+2)
	}

	// Motion extends the selection.
	a.Update(tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: x + 4, Y: y})
	if a.sel.cursor != (components.Pos{Line: idx, Col: sl.Col + 6}) {
		t.Fatalf("cursor = %v, want %v", a.sel.cursor, components.Pos{Line: idx, Col: sl.Col + 6})
	}

	// Release copies the clean underlying text.
	_, cmd = a.Update(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonNone, X: x + 4, Y: y})
	if a.sel.dragging {
		t.Fatal("drag must end on release")
	}
	if !a.sel.active {
		t.Fatal("a non-empty selection stays active after release")
	}
	if cmd == nil {
		t.Fatal("release of a non-empty selection must return a copy cmd")
	}
	want := lm.Text(a.sel.anchor, a.sel.cursor)
	if want != "llo" {
		t.Fatalf("copied text = %q, want %q", want, "llo")
	}
}

func TestMouseWheelDoesNotAnchorSelection(t *testing.T) {
	a, idx := seededChatApp(t, "hello selection world")
	x := a.lastFrame.left + 3
	y := a.lastFrame.top + (idx - a.lastFrame.yOffset)

	a.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp, X: x, Y: y})
	a.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown, X: x, Y: y})
	if a.sel.dragging || a.sel.active || !a.sel.empty() {
		t.Fatalf("wheel anchored a selection: %+v", a.sel)
	}
}

func TestReleaseWithButtonNoneEndsDrag(t *testing.T) {
	a, idx := seededChatApp(t, "hello selection world")
	x := a.lastFrame.left + 3
	y := a.lastFrame.top + (idx - a.lastFrame.yOffset)

	a.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: y})
	a.Update(tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: x + 3, Y: y})
	// X10 reports the release with Button==None; the drag must still end and
	// still copy.
	_, cmd := a.Update(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonNone, X: x + 3, Y: y})
	if a.sel.dragging {
		t.Fatal("release with Button==None must end the drag")
	}
	if cmd == nil {
		t.Fatal("release must copy")
	}
}

func TestBareClickClearsSelection(t *testing.T) {
	a, idx := seededChatApp(t, "hello selection world")
	x := a.lastFrame.left + 3
	y := a.lastFrame.top + (idx - a.lastFrame.yOffset)

	a.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: y})
	a.Update(tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: x + 2, Y: y})
	if !a.sel.active {
		t.Fatal("expected an active selection before the click")
	}
	// A press elsewhere followed by a release at the anchor cell (bare click)
	// clears the selection and copies nothing.
	a.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: y})
	_, cmd := a.Update(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: x, Y: y})
	if a.sel.active || a.sel.dragging || !a.sel.empty() {
		t.Fatalf("bare click must clear: %+v", a.sel)
	}
	if cmd != nil {
		t.Fatal("bare click must not copy")
	}
}

func TestPressOutsideViewportClearsSelection(t *testing.T) {
	a, idx := seededChatApp(t, "hello selection world")
	x := a.lastFrame.left + 3
	y := a.lastFrame.top + (idx - a.lastFrame.yOffset)

	a.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: y})
	a.Update(tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: x + 2, Y: y})
	// Press on the composer (below the viewport rect) clears the selection.
	a.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: a.height - 3})
	if a.sel.active || a.sel.dragging {
		t.Fatalf("press outside viewport must clear: %+v", a.sel)
	}
}

func TestContentChangeClearsSelection(t *testing.T) {
	a, idx := seededChatApp(t, "hello selection world")
	x := a.lastFrame.left + 3
	y := a.lastFrame.top + (idx - a.lastFrame.yOffset)

	a.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: y})
	a.Update(tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: x + 2, Y: y})
	if !a.sel.active {
		t.Fatal("expected an active selection")
	}

	// A new message changes the rendered body: the selection must self-clear.
	a.messages = append(a.messages, components.Message{Role: "system", Content: "new activity"})
	a.View()
	if a.sel.active || a.sel.dragging || !a.sel.empty() {
		t.Fatalf("content change must clear the selection: %+v", a.sel)
	}
}

func TestEscClearsLiveSelection(t *testing.T) {
	a, idx := seededChatApp(t, "hello selection world")
	x := a.lastFrame.left + 3
	y := a.lastFrame.top + (idx - a.lastFrame.yOffset)

	a.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: y})
	a.Update(tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: x + 2, Y: y})
	if !a.sel.active {
		t.Fatal("expected an active selection")
	}

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEsc})
	if a.sel.active || a.sel.dragging {
		t.Fatalf("esc must clear a live selection: %+v", a.sel)
	}
}

func TestWindowSizeClearsSelection(t *testing.T) {
	a, idx := seededChatApp(t, "hello selection world")
	x := a.lastFrame.left + 3
	y := a.lastFrame.top + (idx - a.lastFrame.yOffset)

	a.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: y})
	a.Update(tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: x + 2, Y: y})
	if !a.sel.active {
		t.Fatal("expected an active selection")
	}

	// A height-only resize leaves the body identical, so the content compare
	// cannot catch it: the WindowSizeMsg case clears explicitly.
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	if a.sel.active || a.sel.dragging {
		t.Fatalf("resize must clear the selection: %+v", a.sel)
	}
}
