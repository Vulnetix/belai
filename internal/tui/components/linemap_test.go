package components

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestOrderNormalisesDragDirection(t *testing.T) {
	a := Pos{Line: 3, Col: 5}
	b := Pos{Line: 1, Col: 9}

	from, to := Order(a, b)
	if from != b || to != a {
		t.Fatalf("upward drag not normalised: from=%v to=%v", from, to)
	}

	// Same line, leftward drag.
	l, r := Pos{Line: 2, Col: 8}, Pos{Line: 2, Col: 2}
	from, to = Order(l, r)
	if from != r || to != l {
		t.Fatalf("leftward drag not normalised: from=%v to=%v", from, to)
	}

	// Already ordered pairs are returned untouched.
	from, to = Order(r, l)
	if from != r || to != l {
		t.Fatalf("ordered pair changed: from=%v to=%v", from, to)
	}
}

func TestLineMapTextSingleLineRange(t *testing.T) {
	lm := LineMap{{Text: "hello world", Col: 2, Width: 11}}

	if got := lm.Text(Pos{0, 2}, Pos{0, 7}); got != "hello" {
		t.Fatalf("got %q, want %q", got, "hello")
	}
	// Columns outside the selectable region clamp to it.
	if got := lm.Text(Pos{0, 0}, Pos{0, 99}); got != "hello world" {
		t.Fatalf("clamped range got %q", got)
	}
}

func TestLineMapTextEmptyRange(t *testing.T) {
	lm := LineMap{{Text: "hello", Col: 0, Width: 5}}
	if got := lm.Text(Pos{0, 3}, Pos{0, 3}); got != "" {
		t.Fatalf("zero-width range got %q, want empty", got)
	}
}

func TestLineMapTextDropsChromeAndCollapsesBlanks(t *testing.T) {
	lm := LineMap{
		{Chrome: true},
		{Text: "first", Col: 2, Width: 5},
		{Chrome: true},
		{Chrome: true},
		{Text: "second", Col: 2, Width: 6},
		{Chrome: true},
	}

	got := lm.Text(Pos{0, 0}, Pos{5, 99})
	want := "first\n\nsecond"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestLineMapTextSubstitutesHiddenForMarker(t *testing.T) {
	lm := LineMap{{
		Text:        "… 3 more lines",
		Col:         2,
		Width:       14,
		MarkerCol:   2,
		MarkerWidth: 14,
		Hidden:      "line four\nline five\nline six",
	}}

	got := lm.Text(Pos{0, 2}, Pos{0, 16})
	want := "\nline four\nline five\nline six"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if strings.Contains(got, "more lines") {
		t.Fatal("the marker text must never reach the copy")
	}
}

func TestLineMapTextKeepsVisibleTextBeforeMarker(t *testing.T) {
	lm := LineMap{{
		Text:        "head … 2 more",
		Col:         0,
		Width:       13,
		MarkerCol:   5,
		MarkerWidth: 8,
		Hidden:      "tail",
	}}

	got := lm.Text(Pos{0, 0}, Pos{0, 13})
	if got != "head\ntail" {
		t.Fatalf("got %q, want %q", got, "head\ntail")
	}
}

func TestLineMapTextSlicesOnCellBoundaries(t *testing.T) {
	// Two double-width runes: cells 0-1 and 2-3.
	lm := LineMap{{Text: "日本", Col: 0, Width: 4}}

	if got := lm.Text(Pos{0, 0}, Pos{0, 2}); got != "日" {
		t.Fatalf("got %q, want %q", got, "日")
	}
	if got := lm.Text(Pos{0, 0}, Pos{0, 4}); got != "日本" {
		t.Fatalf("got %q, want %q", got, "日本")
	}
}

func TestLineMapTextIgnoresOutOfRangeLines(t *testing.T) {
	lm := LineMap{{Text: "only", Col: 0, Width: 4}}
	if got := lm.Text(Pos{-2, 0}, Pos{9, 4}); got != "only" {
		t.Fatalf("got %q, want %q", got, "only")
	}
}

func TestHighlightPreservesVisibleTextAndWidth(t *testing.T) {
	styled := lipgloss.NewStyle().Foreground(ColorTeal).Render("hello") + " world"
	lm := LineMap{{Text: ansi.Strip(styled), Col: 0, Width: visibleLen(ansi.Strip(styled))}}

	// Styling depends on the terminal profile, which is inert under `go test`.
	// The invariant that must hold either way is that highlighting changes no
	// visible character and no cell width — a width change would trip the
	// viewport's MaxWidth and shift the whole frame.
	for _, span := range [][2]Pos{
		{{0, 0}, {0, 5}},
		{{0, 2}, {0, 9}},
		{{0, 0}, {0, 11}},
	} {
		out := Highlight(styled, lm, span[0], span[1], 0, 1)
		if ansi.Strip(out) != ansi.Strip(styled) {
			t.Fatalf("highlight changed visible text: %q vs %q", ansi.Strip(out), ansi.Strip(styled))
		}
		if visibleLen(ansi.Strip(out)) != visibleLen(ansi.Strip(styled)) {
			t.Fatalf("highlight changed the line's cell width for span %v", span)
		}
	}
}

func TestHighlightSkipsChromeAndOffscreenLines(t *testing.T) {
	rendered := "chrome\nbody"
	lm := LineMap{{Chrome: true}, {Text: "body", Col: 0, Width: 4}}

	if got := Highlight(rendered, lm, Pos{0, 0}, Pos{0, 6}, 0, 2); got != rendered {
		t.Fatalf("chrome line was highlighted: %q", got)
	}
	// The body line is below the visible window.
	if got := Highlight(rendered, lm, Pos{1, 0}, Pos{1, 4}, 0, 1); got != rendered {
		t.Fatalf("off-screen line was highlighted: %q", got)
	}
}

func TestHighlightRefusesDesyncedMap(t *testing.T) {
	rendered := "one\ntwo"
	lm := LineMap{{Text: "one", Col: 0, Width: 3}}

	if got := Highlight(rendered, lm, Pos{0, 0}, Pos{0, 3}, 0, 2); got != rendered {
		t.Fatal("a map that does not match the frame must leave the frame untouched")
	}
}

func TestPanelRenderMapMatchesFrame(t *testing.T) {
	p := Panel{Title: "t", Body: "alpha\nbeta", Width: 40}
	out, lm := p.Render()

	lines := strings.Split(out, "\n")
	if len(lm) != len(lines) {
		t.Fatalf("map has %d entries for %d lines", len(lm), len(lines))
	}
	if out != p.View() {
		t.Fatal("View and Render disagree on the rendered text")
	}
	if !lm[0].Chrome || !lm[len(lm)-1].Chrome {
		t.Fatal("panel border rows must be marked chrome")
	}

	// The documented invariant: cutting the stripped line at the recorded
	// column range reproduces the recorded text.
	for i, sl := range lm {
		if sl.Chrome {
			continue
		}
		cut := ansi.Cut(ansi.Strip(lines[i]), sl.Col, sl.Col+sl.Width)
		if cut != sl.Text {
			t.Fatalf("line %d: cut %q != mapped %q", i, cut, sl.Text)
		}
	}
}

func TestPanelRenderAttachesMarkerToItsLine(t *testing.T) {
	p := Panel{
		Title:  "t",
		Body:   "visible\n… 2 more lines",
		Width:  40,
		Marker: "… 2 more lines",
		Hidden: "hidden one\nhidden two",
	}
	out, lm := p.Render()

	var markers int
	for i, sl := range lm {
		if sl.MarkerWidth == 0 {
			continue
		}
		markers++
		if !strings.Contains(ansi.Strip(strings.Split(out, "\n")[i]), "2 more lines") {
			t.Fatalf("marker recorded on line %d, which is not the marker line", i)
		}
		if sl.Hidden != "hidden one\nhidden two" {
			t.Fatalf("marker line carries Hidden %q", sl.Hidden)
		}
	}
	if markers != 1 {
		t.Fatalf("%d marker lines recorded, want 1", markers)
	}
}

func TestPanelRenderWithoutMarkerRecordsNone(t *testing.T) {
	_, lm := Panel{Title: "t", Body: "alpha\nbeta", Width: 40}.Render()
	for i, sl := range lm {
		if sl.MarkerWidth != 0 {
			t.Fatalf("line %d recorded a marker with no Marker set", i)
		}
	}
}
