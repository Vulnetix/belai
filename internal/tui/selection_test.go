package tui

import (
	"testing"

	"github.com/vulnetix/signet/internal/tui/components"
)

func testFrame() frame {
	return frame{
		lines:   make(components.LineMap, 20),
		top:     3,
		left:    2,
		width:   40,
		height:  10,
		yOffset: 5,
	}
}

func TestContentPosMapsScreenCellToContentLine(t *testing.T) {
	f := testFrame()

	// Top-left cell of the viewport is the first visible content line.
	got, ok := contentPos(f.left, f.top, f)
	if !ok {
		t.Fatal("top-left viewport cell must be inside the frame")
	}
	if got != (components.Pos{Line: f.yOffset, Col: 0}) {
		t.Fatalf("got %+v, want line %d col 0", got, f.yOffset)
	}

	// Scroll offset is what makes a selection survive scrolling: the same
	// screen row maps to a different content line.
	got, _ = contentPos(f.left+7, f.top+4, f)
	if got != (components.Pos{Line: f.yOffset + 4, Col: 7}) {
		t.Fatalf("got %+v, want line %d col 7", got, f.yOffset+4)
	}
}

func TestContentPosRejectsCellsOutsideTheFrame(t *testing.T) {
	f := testFrame()

	cases := []struct {
		name string
		x, y int
	}{
		{"above", f.left, f.top - 1},
		{"below", f.left, f.top + f.height},
		{"left", f.left - 1, f.top},
		{"right", f.left + f.width, f.top},
	}
	for _, tc := range cases {
		if _, ok := contentPos(tc.x, tc.y, f); ok {
			t.Fatalf("%s: cell outside the frame reported as inside", tc.name)
		}
	}

	// The far corners are inside — the rectangle is half-open on both axes.
	if _, ok := contentPos(f.left+f.width-1, f.top+f.height-1, f); !ok {
		t.Fatal("bottom-right cell must be inside the frame")
	}
}

func TestClampPosPullsAnOutOfFrameCellToTheEdge(t *testing.T) {
	f := testFrame()

	// Parked to the left of the frame: the column clamps to 0 rather than
	// going negative.
	if got := clampPos(f.left-10, f.top+2, f); got.Col != 0 {
		t.Fatalf("Col = %d, want 0", got.Col)
	}

	// Parked above the transcript: the line clamps to the first content line.
	if got := clampPos(f.left, f.top-100, f); got.Line != 0 {
		t.Fatalf("Line = %d, want 0", got.Line)
	}

	// Parked below: the line clamps to the last mapped line, never past it.
	if got := clampPos(f.left, f.top+1000, f); got.Line != len(f.lines)-1 {
		t.Fatalf("Line = %d, want %d", got.Line, len(f.lines)-1)
	}
}

func TestClampPosWithAnEmptyMap(t *testing.T) {
	f := testFrame()
	f.lines = nil

	// No map means no line to clamp to; the caller still gets a usable zero.
	if got := clampPos(f.left, f.top+1000, f); got.Line < 0 {
		t.Fatalf("Line = %d, want a non-negative line", got.Line)
	}
}

func TestSelectionEmptyOnlyWhenAnchorAndCursorAgree(t *testing.T) {
	s := selection{anchor: components.Pos{Line: 2, Col: 4}, cursor: components.Pos{Line: 2, Col: 4}}
	if !s.empty() {
		t.Fatal("a drag that covers no cells is empty")
	}

	s.cursor = components.Pos{Line: 2, Col: 5}
	if s.empty() {
		t.Fatal("a one-cell drag is not empty")
	}

	// Direction does not matter: empty is about coverage, not ordering.
	s.anchor, s.cursor = s.cursor, s.anchor
	if s.empty() {
		t.Fatal("a backwards one-cell drag is not empty")
	}
}
