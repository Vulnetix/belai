package components

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

const goSample = `package main

// greet says hello.
func greet(name string) string {
    return "hello " + name
}
`

func TestHighlightedNeedsAKnownExtension(t *testing.T) {
	withProfile(t, termenv.TrueColor, func() {
		if got := Highlighted("notes.unknownext", goSample); got != nil {
			t.Fatalf("unknown extension should not be highlighted, got %d lines", len(got))
		}
		if got := Highlighted("", goSample); got != nil {
			t.Fatal("empty path should not be highlighted")
		}
		if got := Highlighted("main.go", goSample); got == nil {
			t.Fatal("a .go file should be highlighted")
		}
	})
}

// TestHighlightedIsDisabledWithoutColour keeps test output and piped output
// free of escape sequences.
func TestHighlightedIsDisabledWithoutColour(t *testing.T) {
	withProfile(t, termenv.Ascii, func() {
		if got := Highlighted("main.go", goSample); got != nil {
			t.Fatal("highlighting should be off without colour support")
		}
	})
}

// TestHighlightedPreservesText is the property that matters most: colour may
// change how source looks, never what it says.
func TestHighlightedPreservesText(t *testing.T) {
	withProfile(t, termenv.TrueColor, func() {
		lines := Highlighted("main.go", goSample)
		if lines == nil {
			t.Fatal("expected highlighting")
		}
		var got strings.Builder
		for i, segs := range lines {
			if i > 0 {
				got.WriteString("\n")
			}
			for _, s := range segs {
				got.WriteString(s.Text)
			}
		}
		if got.String() != goSample {
			t.Fatalf("highlighting altered the source:\n got %q\nwant %q", got.String(), goSample)
		}
	})
}

func TestHighlightedUsesThePalette(t *testing.T) {
	withProfile(t, termenv.TrueColor, func() {
		lines := Highlighted("main.go", goSample)
		var kw, str, comment bool
		for _, segs := range lines {
			for _, s := range segs {
				switch s.FG {
				case ColorTeal:
					if s.Text == "func" || s.Text == "package" || s.Text == "return" {
						kw = true
					}
				case ColorAmber:
					if strings.Contains(s.Text, "hello") {
						str = true
					}
				case ColorMuted:
					if strings.Contains(s.Text, "greet says hello") {
						comment = true
					}
				}
			}
		}
		if !kw || !str || !comment {
			t.Fatalf("palette mapping missed: keyword=%v string=%v comment=%v", kw, str, comment)
		}
	})
}

// TestHighlightedCachesByContent: the cache is keyed by content hash, so it
// can never be stale, and a repeated render must not re-lex.
func TestHighlightedCachesByContent(t *testing.T) {
	withProfile(t, termenv.TrueColor, func() {
		ResetSyntaxCache()
		a := Highlighted("main.go", goSample)
		b := Highlighted("main.go", goSample)
		if len(a) == 0 || len(a) != len(b) {
			t.Fatal("expected identical highlights")
		}
		if &a[0] != &b[0] {
			t.Fatal("second call should come from the cache")
		}
		// Different content must not collide.
		c := Highlighted("main.go", goSample+"\n// more\n")
		if len(c) == len(a) {
			t.Fatal("changed content returned the cached highlight")
		}
	})
}

// TestReadRowIsHighlightedOnlyWhenExpanded pins the rule that collapsed rows
// stay plain: three lines of source do not need syntax colour, and it would
// compete with the status and diff colours that carry meaning there.
func TestReadRowIsHighlightedOnlyWhenExpanded(t *testing.T) {
	msg := Message{
		Role: "tool", ToolName: "Read", ToolArgs: `{"path":"main.go"}`,
		Content: goSample, Status: "✓",
	}
	withProfile(t, termenv.TrueColor, func() {
		collapsed, _ := toolRow(msg, 80, false)
		expanded, _ := toolRow(msg, 80, true)

		body := func(out string) string {
			return strings.Join(strings.Split(out, "\n")[1:], "\n")
		}
		if strings.Contains(body(collapsed), fgSeq(ColorTeal)) {
			t.Fatalf("collapsed row should not be syntax highlighted:\n%q", collapsed)
		}
		if !strings.Contains(body(expanded), fgSeq(ColorTeal)) {
			t.Fatalf("expanded row should be syntax highlighted:\n%q", expanded)
		}
		// Colour must not change the text or the width.
		if !strings.Contains(ansi.Strip(expanded), "func greet(name string) string {") {
			t.Fatalf("highlighting altered the source:\n%s", ansi.Strip(expanded))
		}
	})
}

// TestReadRowTabsExpandRelativeToTheCodeColumn: the terminal measures its own
// tab stops from the screen edge, which the gutter would throw off, so tabs
// are expanded here instead.
func TestReadRowTabsExpandRelativeToTheCodeColumn(t *testing.T) {
	msg := Message{
		Role: "tool", ToolName: "Read", ToolArgs: `{"path":"main.go"}`,
		Content: "func f() {\n\treturn\n}", Status: "✓",
	}
	out, _ := toolRow(msg, 80, true)
	if strings.Contains(out, "\t") {
		t.Fatalf("tabs reached the terminal:\n%q", out)
	}
	if !strings.Contains(out, "    return") {
		t.Fatalf("tab was not expanded to the code column:\n%s", out)
	}
}
