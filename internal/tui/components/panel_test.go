package components

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// TestPanelRenderLineMapInvariant pins the line-map contract: the map has one
// entry per rendered line, chrome entries mark the frame, and every
// selectable entry satisfies
//
//	ansi.Cut(ansi.Strip(renderedLine), Col, Col+Width) == Text
//
// across ASCII, CJK and emoji bodies and a range of widths.
func TestPanelRenderLineMapInvariant(t *testing.T) {
	bodies := []string{
		"hello world",
		"日本語のテキストです",
		"emoji 🎉🐱 body",
		"line one\nline two\nline three",
		"a\nb",
		"",
	}
	widths := []int{24, 40, 80, 120}
	for _, width := range widths {
		for _, body := range bodies {
			p := Panel{Title: "t", Body: body, Width: width}
			rendered, lm := p.Render()
			lines := strings.Split(rendered, "\n")
			if len(lm) != len(lines) {
				t.Fatalf("width %d body %q: map has %d entries, rendered %d lines", width, body, len(lm), len(lines))
			}
			if len(lm) != lipgloss.Height(rendered) {
				t.Fatalf("width %d body %q: map %d != height %d", width, body, len(lm), lipgloss.Height(rendered))
			}
			if !lm[0].Chrome || !lm[len(lm)-1].Chrome {
				t.Fatalf("width %d body %q: top/bottom edges must be chrome", width, body)
			}
			for i, sl := range lm {
				if sl.Chrome {
					continue
				}
				got := ansi.Cut(ansi.Strip(lines[i]), sl.Col, sl.Col+sl.Width)
				if got != sl.Text {
					t.Fatalf("width %d body %q line %d: invariant violated: Cut = %q, Text = %q",
						width, body, i, got, sl.Text)
				}
				if strings.ContainsAny(sl.Text, "│╭╮╰╯") {
					t.Fatalf("width %d body %q line %d: border glyph in Text: %q", width, body, i, sl.Text)
				}
				if strings.ContainsRune(sl.Text, 0x1b) {
					t.Fatalf("width %d body %q line %d: ANSI in Text: %q", width, body, i, sl.Text)
				}
			}
			// View and Render agree byte-for-byte.
			if p.View() != rendered {
				t.Fatalf("width %d body %q: View() and Render() diverge", width, body)
			}
		}
	}
}

// TestPanelRenderTruncatesLongBody checks that a body wider than the inner
// width is clipped and the map follows the clipped line.
func TestPanelRenderTruncatesLongBody(t *testing.T) {
	body := strings.Repeat("x", 100)
	p := Panel{Title: "t", Body: body, Width: 40}
	rendered, lm := p.Render()
	lines := strings.Split(rendered, "\n")
	mid := lm[1]
	if mid.Width > 40-4 {
		t.Fatalf("width %d exceeds inner width", mid.Width)
	}
	got := ansi.Cut(ansi.Strip(lines[1]), mid.Col, mid.Col+mid.Width)
	if got != mid.Text {
		t.Fatalf("invariant violated after truncation: %q vs %q", got, mid.Text)
	}
}

// TestPanelRenderCarriesMarker verifies a whole-line truncation hint carries
// its hidden remainder, attached to the line whose text equals the marker.
func TestPanelRenderCarriesMarker(t *testing.T) {
	hidden := "line five\nline six"
	body := "one\ntwo\n" + MutedStyle.Render("… 2 more lines")
	p := Panel{Title: "t", Body: body, Width: 60, Marker: "… 2 more lines", Hidden: hidden}
	_, lm := p.Render()

	var marked *SourceLine
	for i := range lm {
		if lm[i].MarkerWidth > 0 {
			marked = &lm[i]
		}
	}
	if marked == nil {
		t.Fatalf("no line carried the marker: %+v", lm)
	}
	if marked.Hidden != hidden {
		t.Fatalf("Hidden = %q, want %q", marked.Hidden, hidden)
	}
	if marked.Text != "… 2 more lines" {
		t.Fatalf("marker line Text = %q", marked.Text)
	}

	// Selecting the whole panel body yields the visible lines plus the hidden
	// remainder, and never the "… 2 more lines" marker itself.
	first, last := 1, len(lm)-2
	got := lm.Text(Pos{Line: first, Col: 0}, Pos{Line: last, Col: lastCol(lm)})
	want := "one\ntwo\n" + hidden
	if got != want {
		t.Fatalf("full-body copy = %q, want %q", got, want)
	}
	// A selection stopping above the marker copies only what is visible.
	above := last - 1
	if got := lm.Text(Pos{Line: first, Col: 0}, Pos{Line: above, Col: lastCol(lm)}); got != "one\ntwo" {
		t.Fatalf("pre-marker copy = %q, want %q", got, "one\ntwo")
	}
}

// lastCol returns one past the right edge of the selectable region of line i.
func lastCol(lm LineMap) int {
	sl := lm[len(lm)-2]
	return sl.Col + sl.Width
}
