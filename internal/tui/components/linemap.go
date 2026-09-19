package components

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Pos is a cell coordinate in the rendered transcript. Line is a content
// line — zero-based and absolute across the whole MessageList render,
// independent of the viewport's scroll offset, so a selection survives
// scrolling untouched. Col is a terminal cell column, zero-based: cells,
// never runes, so CJK and emoji land on cluster boundaries.
type Pos struct{ Line, Col int }

// SourceLine is the clean-text provenance of one rendered line. Invariant:
//
//	ansi.Cut(ansi.Strip(renderedLine), Col, Col+Width) == Text
//
// for every non-chrome line, where renderedLine is the corresponding line of
// the frame's styled text. Text carries no ANSI, no border glyphs, no
// prefixes and no padding — only what the user meant when they pointed at
// that stretch of screen.
type SourceLine struct {
	Text  string // clean displayed text
	Col   int    // screen column where Text begins
	Width int    // cell width of Text

	// Marker delimits an on-screen truncation hint ("… N more lines"). A
	// selection overlapping [MarkerCol, MarkerCol+MarkerWidth) substitutes
	// Hidden into the copy, which is what makes a selection over a collapsed
	// result yield the whole result. Zero width means no marker.
	MarkerCol, MarkerWidth int
	Hidden                 string

	// Reopen is the SGR prefix that restores the row's original styling
	// after a mid-line reset. It is used by Highlight so a selection over a
	// washed or syntax-coloured row does not leave the remainder bare.
	Reopen string

	// Chrome marks pure frame (panel edges, blank separators). Chrome lines
	// contribute a blank to copies and are never highlighted.
	Chrome bool

	// Owner is the index of the MessageList.Message that rendered this line,
	// or -1 for separator chrome between messages. Hover hit-testing uses it
	// to recover which panel the pointer is over.
	Owner int

	// File marks a line belonging to a file panel: a Read result that carries
	// a path, whose content is a file the thread has output. Hovering such a
	// panel offers save-to-disk and copy-to-clipboard.
	File bool

	// Copyable marks a line belonging to a panel that has text to hand out.
	// Hovering such a panel offers copy-to-clipboard and save-to-disk. File
	// panels are the subset whose save hint names a real file.
	Copyable bool

	// Collapsed marks a line belonging to a panel that is currently truncated
	// (it carries a "… N more lines" hint). Hovering such a panel offers
	// ctrl+o to expand every truncated panel.
	Collapsed bool
}

// LineMap is the per-line provenance of one rendered transcript frame.
// Invariant: len(lm) == strings.Count(rendered, "\n") + 1 — one content line
// maps 1:1 to one screen row because the viewport's SetContent splits on "\n"
// without re-wrapping and this repo never sets a viewport style.
type LineMap []SourceLine

// Order normalises a possibly upward- or leftward-dragged pair into (from,
// to), so callers never reason about drag direction.
func Order(a, b Pos) (from, to Pos) {
	if a.Line < b.Line || (a.Line == b.Line && a.Col <= b.Col) {
		return a, b
	}
	return b, a
}

// clampCell clamps a column to the line's selectable region.
func (sl SourceLine) clampCell(c int) int {
	if c < sl.Col {
		return sl.Col
	}
	if c > sl.Col+sl.Width {
		return sl.Col + sl.Width
	}
	return c
}

// Text returns the clean text of the half-open cell range [from, to).
// Chrome lines and lines with no selectable width contribute a blank line;
// runs of blanks collapse to one and leading and trailing blanks are dropped,
// so a span across two panels reads as two paragraphs, not three.
//
// The range is clamped per line to [Col, Col+Width), and slicing goes through
// ansi.Cut — cells, never []rune indexing — so CJK and emoji stay on cluster
// boundaries. When the range overlaps a line's marker, the marker is
// substituted with Hidden: the visible text before the marker, then the
// hidden remainder.
func (lm LineMap) Text(from, to Pos) string {
	from, to = Order(from, to)
	if from.Line == to.Line && from.Col >= to.Col {
		return ""
	}
	var out []string
	for line := from.Line; line <= to.Line; line++ {
		if line < 0 || line >= len(lm) {
			continue
		}
		sl := lm[line]
		lo, hi := sl.Col, sl.Col+sl.Width
		if line == from.Line {
			lo = from.Col
		}
		if line == to.Line {
			hi = to.Col
		}
		lo, hi = sl.clampCell(lo), sl.clampCell(hi)

		text := ""
		if !sl.Chrome && sl.Width > 0 && hi > lo {
			text = strings.TrimRight(ansi.Cut(sl.Text, lo-sl.Col, hi-sl.Col), " ")
			if sl.MarkerWidth > 0 && sl.Hidden != "" &&
				lo < sl.MarkerCol+sl.MarkerWidth && hi > sl.MarkerCol {
				// Substitution: keep only the visible text before the marker,
				// then the hidden remainder. The marker text itself never
				// reaches the copy.
				end := sl.clampCell(sl.MarkerCol)
				var pre string
				if end > lo {
					pre = strings.TrimRight(ansi.Cut(sl.Text, lo-sl.Col, end-sl.Col), " ")
				}
				if pre == "" {
					text = sl.Hidden
				} else {
					text = pre + "\n" + sl.Hidden
				}
			}
		}
		out = append(out, text)
	}
	return collapseBlanks(out)
}

// collapseBlanks drops leading and trailing blank entries and collapses runs
// of interior blanks to a single one.
func collapseBlanks(lines []string) string {
	n := 0
	for n < len(lines) && lines[n] == "" {
		n++
	}
	e := len(lines)
	for e > n && lines[e-1] == "" {
		e--
	}
	var b strings.Builder
	prevBlank := false
	for i := n; i < e; i++ {
		if lines[i] == "" {
			if prevBlank || b.Len() == 0 {
				continue
			}
			b.WriteString("\n")
			prevBlank = true
			continue
		}
		if b.Len() > 0 && !prevBlank {
			b.WriteString("\n")
		}
		b.WriteString(lines[i])
		prevBlank = false
	}
	return b.String()
}

// Highlight reverse-videos the range [from, to) in rendered. Only lines in
// [topLine, topLine+height) are rewritten — the rest is off-screen, and
// chatView re-renders every frame, so rewriting visible lines is enough.
//
// The middle is stripped and re-rendered under SelectionStyle deliberately:
// lipgloss emits a reset at the end of every styled span, so a selection
// crossing a style boundary (muted invocation to accent status, teal bar to
// cream body) would otherwise have its bare reverse-video wrapper cancelled
// halfway through. ansi.Cut passes escape bytes through while counting cells,
// so the unselected head and tail keep their original colours. The cost is
// flat colour per token inside the selection — normal for a selection.
//
// Highlighting must not change the visible characters or any line's cell
// width: a width change would trip the viewport's MaxWidth and shift the
// frame.
func Highlight(rendered string, lm LineMap, from, to Pos, topLine, height int) string {
	from, to = Order(from, to)
	lines := strings.Split(rendered, "\n")
	if len(lines) != len(lm) {
		return rendered // map and frame out of sync; never corrupt the frame
	}
	bottom := topLine + height
	if bottom > len(lines) {
		bottom = len(lines)
	}
	for i := from.Line; i <= to.Line; i++ {
		if i < topLine || i >= bottom || i < 0 || i >= len(lines) {
			continue
		}
		sl := lm[i]
		if sl.Chrome || sl.Width == 0 {
			continue
		}
		lo, hi := sl.Col, sl.Col+sl.Width
		if i == from.Line {
			lo = from.Col
		}
		if i == to.Line {
			hi = to.Col
		}
		lo, hi = sl.clampCell(lo), sl.clampCell(hi)
		if hi <= lo {
			continue
		}
		line := lines[i]
		left := ansi.Cut(line, 0, lo)
		mid := SelectionStyle.Render(ansi.Strip(ansi.Cut(line, lo, hi)))
		right := ansi.Cut(line, hi, visibleLen(ansi.Strip(line)))
		// The mid span closes with a full reset, so we restore the row's
		// original SGR state before the right fragment so the remainder is not
		// left bare — critical for washed diff rows.
		lines[i] = left + mid + "\x1b[39m\x1b[49m" + sl.Reopen + right
	}
	return strings.Join(lines, "\n")
}
