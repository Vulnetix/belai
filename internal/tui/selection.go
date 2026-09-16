package tui

import (
	"github.com/vulnetix/signet/internal/tui/components"
)

// frame is the geometry and provenance of the last rendered transcript frame.
// Mouse coordinates describe the frame the user was looking at, so hit-testing
// reads this snapshot rather than live state: chatView rebuilds the frame
// after every message change, and the snapshot is refreshed at the end of
// that rebuild (after GotoBottom, which mutates YOffset).
type frame struct {
	lines   components.LineMap
	top     int // screen row of the viewport's first line (headerHeight)
	left    int // screen column of content column 0 (contentLeft)
	width   int
	height  int
	yOffset int // vp.YOffset at render time
}

// selection is a drag in content coordinates — line indices are content
// lines, not screen rows, so a selection survives scrolling untouched and the
// wheel can be used mid-drag (press, wheel, keep dragging, release).
type selection struct {
	anchor, cursor components.Pos
	dragging       bool // a mouse drag is in progress
	active         bool // a non-empty selection is on screen (highlight + esc)
}

// empty reports whether the drag covers no cells (anchor and cursor agree).
func (s selection) empty() bool {
	from, to := components.Order(s.anchor, s.cursor)
	return from.Line == to.Line && from.Col == to.Col
}

// contentPos maps a screen cell to content coordinates. ok is false when the
// cell is outside the viewport rectangle: a press there must not anchor a
// selection (it clears one instead), and motion/release outside are ignored
// by the caller.
func contentPos(x, y int, f frame) (components.Pos, bool) {
	if y < f.top || y >= f.top+f.height || x < f.left || x >= f.left+f.width {
		return components.Pos{}, false
	}
	line := f.yOffset + (y - f.top)
	if line < 0 {
		line = 0
	}
	return components.Pos{Line: line, Col: x - f.left}, true
}

// clampPos maps any screen cell to the nearest in-frame content position. It
// is what a drag edge uses when the pointer parks beyond the frame: the
// selection grows to the edge instead of jumping back to the last in-frame
// cell, and per-line clamping in LineMap.Text/Highlight keeps the copy and
// the highlight inside each line's selectable region.
func clampPos(x, y int, f frame) components.Pos {
	line := f.yOffset + (y - f.top)
	if line < 0 {
		line = 0
	}
	if line >= len(f.lines) && len(f.lines) > 0 {
		line = len(f.lines) - 1
	}
	col := x - f.left
	if col < 0 {
		col = 0
	}
	return components.Pos{Line: line, Col: col}
}
