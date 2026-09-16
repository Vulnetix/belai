package components

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

func TestOrderNormalisesDragDirection(t *testing.T) {
	cases := []struct {
		a, b           Pos
		wantFrom, want Pos
	}{
		{Pos{0, 0}, Pos{1, 2}, Pos{0, 0}, Pos{1, 2}},
		{Pos{1, 2}, Pos{0, 0}, Pos{0, 0}, Pos{1, 2}}, // upward drag
		{Pos{0, 5}, Pos{0, 2}, Pos{0, 2}, Pos{0, 5}}, // leftward drag
		{Pos{2, 0}, Pos{2, 0}, Pos{2, 0}, Pos{2, 0}}, // same cell
		{Pos{1, 9}, Pos{1, 3}, Pos{1, 3}, Pos{1, 9}},
	}
	for i, c := range cases {
		from, to := Order(c.a, c.b)
		if from != c.wantFrom || to != c.want {
			t.Fatalf("case %d: Order(%v, %v) = (%v, %v), want (%v, %v)", i, c.a, c.b, from, to, c.wantFrom, c.want)
		}
	}
}

// sampleMap is a two-paragraph map: line 0 chrome, lines 1-3 selectable text,
// line 4 blank chrome, lines 5-6 selectable text, line 7 chrome.
func sampleMap() LineMap {
	return LineMap{
		{Chrome: true},
		{Col: 2, Width: len("hello"), Text: "hello"},
		{Col: 2, Width: len("world"), Text: "world"},
		{Col: 2, Width: len("foo"), Text: "foo"},
		{Chrome: true},
		{Col: 2, Width: len("alpha"), Text: "alpha"},
		{Col: 2, Width: len("beta"), Text: "beta"},
		{Chrome: true},
	}
}

func TestLineMapTextRanges(t *testing.T) {
	lm := sampleMap()
	// Text sits at screen column 2 (after the "│ " gutter) on every line.
	cases := []struct {
		name     string
		from, to Pos
		want     string
	}{
		{"single full line", Pos{1, 2}, Pos{1, 7}, "hello"},
		{"mid-word range", Pos{1, 3}, Pos{1, 5}, "el"},
		{"zero range", Pos{1, 4}, Pos{1, 4}, ""},
		{"reversed range", Pos{1, 5}, Pos{1, 3}, "el"},
		{"range starting left of Col", Pos{1, 0}, Pos{1, 7}, "hello"},
		{"range past line end", Pos{1, 4}, Pos{1, 99}, "llo"},
		{"two lines", Pos{1, 2}, Pos{2, 7}, "hello\nworld"},
		{"partial both ends", Pos{1, 3}, Pos{2, 5}, "ello\nwor"},
		// A span across the chrome separator reads as two paragraphs: the
		// blank collapses and the chrome contributes nothing.
		{"across panels", Pos{1, 2}, Pos{5, 7}, "hello\nworld\nfoo\nalpha"},
		// Chrome-only span: empty.
		{"chrome only", Pos{0, 0}, Pos{1, 0}, ""},
		// Leading and trailing blanks drop.
		{"blank at edges", Pos{0, 0}, Pos{6, 7}, "hello\nworld\nfoo\nalpha\nbeta"},
		// Interior blank run collapses to one.
		{"blank interior", Pos{1, 2}, Pos{7, 0}, "hello\nworld\nfoo\nalpha\nbeta"},
		// Selection ending inside a line.
		{"end mid line", Pos{5, 4}, Pos{6, 4}, "pha\nbe"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := lm.Text(c.from, c.to); got != c.want {
				t.Fatalf("Text(%v, %v) = %q, want %q", c.from, c.to, got, c.want)
			}
		})
	}
}

func TestLineMapTextWideRunes(t *testing.T) {
	// "日本語abc" at Col 2: 日[2,4) 本[4,6) 語[6,8) a[8,9) b[9,10) c[10,11).
	text := "日本語abc"
	lm := LineMap{{Col: 2, Width: visibleLen(text), Text: text}}

	if got := lm.Text(Pos{0, 2}, Pos{0, 4}); got != "日" {
		t.Fatalf("cells [2,4) = %q, want 日", got)
	}
	if got := lm.Text(Pos{0, 4}, Pos{0, 8}); got != "本語" {
		t.Fatalf("cells [4,8) = %q, want 本語", got)
	}
	if got := lm.Text(Pos{0, 8}, Pos{0, 11}); got != "abc" {
		t.Fatalf("cells [8,11) = %q, want abc", got)
	}
	if got := lm.Text(Pos{0, 6}, Pos{0, 9}); got != "語a" {
		t.Fatalf("cells [6,9) = %q, want 語a", got)
	}
	// A cut that starts mid-cluster snaps to cluster boundaries rather than
	// emitting a half glyph: [3,5) resolves to the complete cluster 日.
	if got := lm.Text(Pos{0, 3}, Pos{0, 5}); got != "日" {
		t.Fatalf("cells [3,5) = %q, want 日 (cluster-aligned)", got)
	}
}

func TestLineMapTextMarkerSubstitution(t *testing.T) {
	// A tool-preview line: "alpha" then an inline "  … 2 more lines" hint.
	// The marker sits inside the line's selectable region.
	hint := "… 2 more lines"
	text := "alpha  " + hint
	col := 2
	mc := col + len("alpha  ")
	lm := LineMap{{
		Col: col, Width: len(text), Text: text,
		MarkerCol: mc, MarkerWidth: len(hint),
		Hidden: "h1\nh2",
	}}
	end := col + len(text)

	// A selection covering the whole line (marker touched) copies the visible
	// text before the hint plus the hidden remainder.
	if got := lm.Text(Pos{0, 0}, Pos{0, end}); got != "alpha\nh1\nh2" {
		t.Fatalf("marker touched = %q, want %q", got, "alpha\nh1\nh2")
	}
	// Stopping exactly at the marker start copies only the visible text.
	if got := lm.Text(Pos{0, 0}, Pos{0, mc}); got != "alpha" {
		t.Fatalf("marker not touched = %q, want %q", got, "alpha")
	}
	// Starting inside the marker still substitutes the hidden remainder.
	if got := lm.Text(Pos{0, mc + 3}, Pos{0, end}); got != "h1\nh2" {
		t.Fatalf("start in marker = %q, want %q", got, "h1\nh2")
	}
	// A selection over the first line of a two-line span where the second line
	// carries the marker: both contributions join cleanly.
	lm = append(lm, SourceLine{Col: col, Width: 4, Text: "next"})
	if got := lm.Text(Pos{0, 0}, Pos{1, col + 4}); got != "alpha\nh1\nh2\nnext" {
		t.Fatalf("two-line span = %q, want %q", got, "alpha\nh1\nh2\nnext")
	}
}

func TestHighlightPreservesVisibleText(t *testing.T) {
	// Build a rendered frame from the sample map: the rendered line for each
	// selectable entry is "│ " + text + padding + " │".
	var lines []string
	for _, sl := range sampleMap() {
		if sl.Chrome {
			lines = append(lines, "────────────────────────────")
		} else {
			lines = append(lines, "│ "+sl.Text+strings.Repeat(" ", 16-sl.Width)+" │")
		}
	}
	rendered := strings.Join(lines, "\n")

	cases := []struct {
		from, to Pos
	}{
		{Pos{1, 2}, Pos{2, 4}}, // crosses a line boundary
		{Pos{1, 0}, Pos{1, 5}}, // full line
		{Pos{0, 0}, Pos{7, 0}}, // includes chrome lines
		{Pos{6, 1}, Pos{6, 3}}, // inside one line
	}
	for i, c := range cases {
		high := Highlight(rendered, sampleMap(), c.from, c.to, 0, len(lines))
		if ansi.Strip(high) != ansi.Strip(rendered) {
			t.Fatalf("case %d: visible text changed:\n%q\nvs\n%q", i, ansi.Strip(high), ansi.Strip(rendered))
		}
		highLines := strings.Split(high, "\n")
		for j := range lines {
			if w, ow := ansi.StringWidth(highLines[j]), ansi.StringWidth(lines[j]); w != ow {
				t.Fatalf("case %d: line %d width changed: %d vs %d", i, j, w, ow)
			}
		}
	}

	// Empty range returns the input untouched.
	if got := Highlight(rendered, sampleMap(), Pos{2, 2}, Pos{2, 2}, 0, len(lines)); got != rendered {
		t.Fatal("empty range must return the input")
	}
}

func TestHighlightOnlySelectedCellsAreReversed(t *testing.T) {
	// lipgloss degrades styles to plain text without a TTY; force a colour
	// profile for the duration of this test so the SGR assertions are real.
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(old) })

	// One line of selectable text at Col 2, Width 5: "│ hello·········· │"
	line := "│ " + "hello" + strings.Repeat(" ", 16-5) + " │"
	lm := LineMap{{Chrome: true}, {Col: 2, Width: 5, Text: "hello"}, {Chrome: true}}
	rendered := "────────\n" + line + "\n" + "────────"

	high := Highlight(rendered, lm, Pos{1, 4}, Pos{1, 7}, 0, 3)
	highLines := strings.Split(high, "\n")

	// The reverse-video SGR (\x1b[7m) must appear exactly once, wrapping cells
	// [4,7) of the text ("llo"), and must not touch the border columns.
	if strings.Count(highLines[1], "\x1b[7m") != 1 {
		t.Fatalf("want exactly one reverse-video SGR, got %d in %q", strings.Count(highLines[1], "\x1b[7m"), highLines[1])
	}
	if ansi.Strip(highLines[1]) != ansi.Strip(line) {
		t.Fatalf("characters shifted: %q vs %q", ansi.Strip(highLines[1]), ansi.Strip(line))
	}
	// No SGR may start inside the border regions.
	if strings.Contains(highLines[1][:2], "\x1b") || strings.Contains(highLines[1][len(highLines[1])-2:], "\x1b") {
		t.Fatalf("style leaked into the border: %q", highLines[1])
	}
}

func TestHighlightSkipsOffscreenLines(t *testing.T) {
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(old) })

	// A 10-line frame, selection spanning lines 0..9, but only lines 3..6 are
	// visible (topLine 3, height 4): only those may be rewritten.
	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, "│ abcdefgh")
	}
	lm := LineMap{}
	for i := 0; i < 10; i++ {
		lm = append(lm, SourceLine{Col: 2, Width: 8, Text: "abcdefgh"})
	}
	rendered := strings.Join(lines, "\n")
	orig := strings.Split(rendered, "\n")

	high := Highlight(rendered, lm, Pos{0, 0}, Pos{9, 8}, 3, 4)
	got := strings.Split(high, "\n")
	for i := range got {
		if i >= 3 && i < 7 {
			if !strings.Contains(got[i], "\x1b[7m") {
				t.Fatalf("line %d should be highlighted: %q", i, got[i])
			}
			if ansi.Strip(got[i]) != ansi.Strip(orig[i]) {
				t.Fatalf("line %d visible text changed", i)
			}
		} else if got[i] != orig[i] {
			t.Fatalf("line %d off-screen was rewritten: %q", i, got[i])
		}
	}
}

func TestHighlightMapFrameMismatchIsSafe(t *testing.T) {
	if got := Highlight("a\nb\n", LineMap{{}}, Pos{0, 0}, Pos{1, 1}, 0, 2); got != "a\nb\n" {
		t.Fatalf("out-of-sync map must return the input, got %q", got)
	}
}
