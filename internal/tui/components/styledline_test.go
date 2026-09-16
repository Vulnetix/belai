package components

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// TestNewSegStripsTerminalControlSequences is the security test for this
// package. Tool output and file contents reach the terminal through Seg; an
// escape sequence that survives could write the user's clipboard (OSC 52),
// move the cursor, or scroll the frame.
func TestNewSegStripsTerminalControlSequences(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"osc52 clipboard write", "before\x1b]52;c;aGVsbG8=\x07after", "before]52;c;aGVsbG8=after"},
		{"csi cursor move", "a\x1b[2Jb", "a[2Jb"},
		{"bare escape", "a\x1bb", "ab"},
		{"c1 csi single byte", "a\x9bXb", "aXb"},
		{"c1 osc single byte", "a\x9dXb", "aXb"},
		{"del", "a\x7fb", "ab"},
		{"bell and backspace", "a\x07\x08b", "ab"},
		{"tab becomes space", "a\tb", "a b"},
		{"newline becomes space", "a\nb", "a b"},
		{"plain text untouched", "func main() {", "func main() {"},
		{"wide runes untouched", "日本語 ok", "日本語 ok"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NewSeg(tc.in, nil).Text
			if got != tc.want {
				t.Fatalf("NewSeg(%q).Text = %q, want %q", tc.in, got, tc.want)
			}
			if strings.ContainsFunc(got, isControl) {
				t.Fatalf("control character survived in %q", got)
			}
		})
	}
}

// TestRowRenderNeverEmitsFullReset guards the layering rule. A \x1b[0m inside
// a row clears the background as well as the foreground, so the wash would
// stop at the first coloured token and the rest of the row would render bare.
// This is the assertion that stops someone reintroducing lipgloss.Render
// inside a Row.
func TestRowRenderNeverEmitsFullReset(t *testing.T) {
	withProfile(t, termenv.TrueColor, func() {
		r := Row{
			Segs: []Seg{
				NewSeg("- 118 ", ColorDanger),
				NewSeg("if ", ColorTeal),
				{Text: "expandAll", Emph: []Span{{From: 0, To: 6}}},
				NewSeg(" {", ColorLine),
			},
			BG:  ColorDiffDelBg,
			Pad: true,
		}
		line, _ := r.Render(40)

		if strings.Contains(line, "\x1b[0m") {
			t.Fatalf("row emitted a full reset:\n%q", line)
		}
		if !strings.Contains(line, bgOff) {
			t.Fatalf("row with a background must close it with %q:\n%q", bgOff, line)
		}
		if strings.Count(line, bgOff) != 1 {
			t.Fatalf("want exactly one background close, got %d:\n%q", strings.Count(line, bgOff), line)
		}
		// The background must open before any foreground, so every span sits
		// inside it.
		if bg := bgSeq(ColorDiffDelBg); !strings.HasPrefix(line, bg) {
			t.Fatalf("row must open with its background %q:\n%q", bg, line)
		}
		if !strings.Contains(line, emphOn) || !strings.Contains(line, emphOff) {
			t.Fatalf("emphasis span did not render as reverse video:\n%q", line)
		}
	})
}

// TestRowRenderWidthAndProvenance checks the two LineMap contracts over a
// matrix of widths and profiles: the styled line is never wider than the
// frame, and its SourceLine describes the plain text exactly.
func TestRowRenderWidthAndProvenance(t *testing.T) {
	rows := []Row{
		{Segs: []Seg{NewSeg("  ", nil), NewSeg("plain body text", nil)}, Gutter: 2},
		{Segs: []Seg{NewSeg("+ 42 ", ColorTeal), NewSeg("added := true", nil)},
			BG: ColorDiffAddBg, Pad: true, Gutter: 5},
		{Segs: []Seg{NewSeg("- 41 ", ColorDanger), NewSeg("removed := false", nil)},
			BG: ColorDiffDelBg, Pad: true, Gutter: 5},
		{Segs: []Seg{NewSeg("  ", nil), NewSeg("日本語のテキストです", nil)}, Gutter: 2},
		{Segs: []Seg{NewSeg("────", nil)}, Chrome: true},
	}

	for _, profile := range renderProfiles {
		withProfile(t, profile, func() {
			for width := 1; width <= 60; width++ {
				for i, r := range rows {
					line, sl := r.Render(width)

					if w := lipgloss.Width(line); w > width {
						t.Fatalf("%s row %d width %d: rendered %d cells: %q",
							profileName(profile), i, width, w, line)
					}
					if strings.Contains(line, "\n") {
						t.Fatalf("%s row %d width %d: Row.Render emitted a newline", profileName(profile), i, width)
					}
					if sl.Chrome {
						continue
					}
					got := ansi.Cut(ansi.Strip(line), sl.Col, sl.Col+sl.Width)
					if got != sl.Text {
						t.Fatalf("%s row %d width %d: invariant violated: Cut=%q Text=%q (line=%q)",
							profileName(profile), i, width, got, sl.Text, line)
					}
					if strings.ContainsRune(sl.Text, 0x1b) {
						t.Fatalf("%s row %d width %d: ANSI in Text: %q",
							profileName(profile), i, width, sl.Text)
					}
				}
			}
		})
	}
}

// TestRowRenderStyledAndPlainAgree pins that styling never changes what the
// user sees, only how it looks.
func TestRowRenderStyledAndPlainAgree(t *testing.T) {
	r := Row{
		Segs: []Seg{
			NewSeg("+ 7 ", ColorTeal),
			NewSeg("return errors.New(\"boom\")", ColorAmber),
		},
		BG:     ColorDiffAddBg,
		Pad:    true,
		Gutter: 4,
	}
	withProfile(t, termenv.TrueColor, func() {
		styled, _ := r.Render(60)
		withProfile(t, termenv.Ascii, func() {
			plain, _ := r.Render(60)
			if ansi.Strip(styled) != plain {
				t.Fatalf("styled and plain diverge:\n styled=%q\n  plain=%q", ansi.Strip(styled), plain)
			}
			if lipgloss.Width(styled) != lipgloss.Width(plain) {
				t.Fatalf("width drift: styled=%d plain=%d", lipgloss.Width(styled), lipgloss.Width(plain))
			}
		})
	})
}

// TestRenderRowsLineCountMatchesMap is the invariant Highlight refuses to run
// without: one map entry per rendered line.
func TestRenderRowsLineCountMatchesMap(t *testing.T) {
	for _, n := range []int{0, 1, 2, 7} {
		rows := make([]Row, n)
		for i := range rows {
			rows[i] = Row{Segs: []Seg{NewSeg("line", nil)}}
		}
		out, lm := renderRows(rows, 20)
		if len(lm) != n {
			t.Fatalf("n=%d: map has %d entries", n, len(lm))
		}
		if n > 0 && len(lm) != strings.Count(out, "\n")+1 {
			t.Fatalf("n=%d: map %d != lines %d", n, len(lm), strings.Count(out, "\n")+1)
		}
	}
}

func TestExpandTabsUsesGutterRelativeStops(t *testing.T) {
	cases := []struct {
		in       string
		startCol int
		want     string
	}{
		{"\tif x {", 0, "    if x {"},
		{"a\tb", 0, "a   b"},
		{"abcd\te", 0, "abcd    e"},
		// Stops are measured from startCol, not from the screen edge, so a
		// gutter does not shift the indentation of the code beside it.
		{"\tif x {", 6, "    if x {"},
		{"no tabs here", 3, "no tabs here"},
	}
	for _, tc := range cases {
		if got := expandTabs(tc.in, tc.startCol, 4); got != tc.want {
			t.Fatalf("expandTabs(%q, %d, 4) = %q, want %q", tc.in, tc.startCol, got, tc.want)
		}
	}
}

func TestRowMarkerSurvivesRender(t *testing.T) {
	r := Row{
		Segs:        []Seg{NewSeg("  ", nil), NewSeg("head  … 3 more lines", nil)},
		Gutter:      2,
		MarkerCol:   8,
		MarkerWidth: 14,
		Hidden:      "a\nb\nc",
	}
	_, sl := r.Render(40)
	if sl.MarkerWidth != 14 || sl.Hidden != "a\nb\nc" {
		t.Fatalf("marker lost: %+v", sl)
	}
}
