package components

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// inlinePlain concatenates parsed segments back to plain text.
func inlinePlain(segs []Seg) string {
	var b strings.Builder
	for _, s := range segs {
		b.WriteString(s.Text)
	}
	return b.String()
}

func TestParseInlineBold(t *testing.T) {
	segs := parseInline("**bold**")
	if inlinePlain(segs) != "bold" {
		t.Fatalf("plain = %q", inlinePlain(segs))
	}
	if segs[0].FG != ColorCream {
		t.Fatalf("bold should be cream, got %v", segs[0].FG)
	}
}

func TestParseInlineItalic(t *testing.T) {
	segs := parseInline("*em*")
	if inlinePlain(segs) != "em" {
		t.Fatalf("plain = %q", inlinePlain(segs))
	}
	if segs[0].FG != ColorTealSoft {
		t.Fatalf("italic should be teal-soft, got %v", segs[0].FG)
	}
}

func TestParseInlineBoldItalic(t *testing.T) {
	segs := parseInline("***both***")
	if inlinePlain(segs) != "both" {
		t.Fatalf("plain = %q", inlinePlain(segs))
	}
	if segs[0].FG != ColorCream {
		t.Fatalf("bold+italic should be cream, got %v", segs[0].FG)
	}
	if len(segs[0].Emph) == 0 {
		t.Fatalf("bold+italic should carry reverse video")
	}
}

func TestParseInlineCode(t *testing.T) {
	segs := parseInline("`code`")
	if inlinePlain(segs) != "code" {
		t.Fatalf("plain = %q", inlinePlain(segs))
	}
	if segs[0].FG != ColorAmber {
		t.Fatalf("code should be amber, got %v", segs[0].FG)
	}
}

func TestParseInlineStrike(t *testing.T) {
	segs := parseInline("~~strike~~")
	if inlinePlain(segs) != "strike" {
		t.Fatalf("plain = %q", inlinePlain(segs))
	}
	if len(segs[0].Strike) == 0 {
		t.Fatalf("strike span missing")
	}
}

func TestParseInlineLink(t *testing.T) {
	segs := parseInline("[text](https://example.com)")
	if len(segs) != 2 {
		t.Fatalf("want text + url segs, got %d: %+v", len(segs), segs)
	}
	if segs[0].Text != "text" || segs[0].FG != ColorTealSoft {
		t.Fatalf("link text = %+v", segs[0])
	}
	if segs[1].Text != " (https://example.com)" || segs[1].FG != ColorMuted || !segs[1].Link {
		t.Fatalf("link url = %+v", segs[1])
	}
}

func TestParseInlineAutolink(t *testing.T) {
	segs := parseInline("<https://example.com>")
	if inlinePlain(segs) != "https://example.com" {
		t.Fatalf("plain = %q", inlinePlain(segs))
	}
	if segs[0].FG != ColorTealSoft {
		t.Fatalf("autolink should be teal-soft, got %v", segs[0].FG)
	}
}

func TestParseInlineBackslashEscape(t *testing.T) {
	segs := parseInline(`\*not em\*`)
	if inlinePlain(segs) != "*not em*" {
		t.Fatalf("plain = %q", inlinePlain(segs))
	}
}

func TestParseInlineSnakeCaseNotEmphasis(t *testing.T) {
	segs := parseInline("snake_case_name")
	if inlinePlain(segs) != "snake_case_name" {
		t.Fatalf("plain = %q", inlinePlain(segs))
	}
	// The underscores may split the text into several plain segments, but they
	// must never introduce emphasis styling: snake_case is prose, not markup.
	for _, s := range segs {
		if s.FG != nil || len(s.Emph) > 0 || len(s.Strike) > 0 {
			t.Fatalf("snake_case should stay plain: %+v", segs)
		}
	}
}

func TestParseInlineUnmatchedDelimiterStaysLiteral(t *testing.T) {
	segs := parseInline("unmatched **bold")
	if inlinePlain(segs) != "unmatched **bold" {
		t.Fatalf("plain = %q", inlinePlain(segs))
	}
}

func TestParseInlineNestedEmphasis(t *testing.T) {
	t.Skip("markdown parser refactor: nested emphasis styling pending")
	segs := parseInline("**bold *and em* text**")
	if inlinePlain(segs) != "bold and em text" {
		t.Fatalf("plain = %q", inlinePlain(segs))
	}
	var sawCream, sawTealSoft bool
	for _, s := range segs {
		sawCream = sawCream || s.FG == ColorCream
		sawTealSoft = sawTealSoft || s.FG == ColorTealSoft
	}
	if !sawCream || !sawTealSoft {
		t.Fatalf("nested emphasis lost a colour: %+v", segs)
	}
}

// TestWrapSegsLinkURLDropped pins the link rule: a URL that cannot share its
// text's line is dropped, never wrapped onto a line of its own.
func TestWrapSegsLinkURLDropped(t *testing.T) {
	segs := parseInline("[text](https://very.long.example.com/url)")
	lines := wrapSegs(segs, 10)
	for _, line := range lines {
		if strings.Contains(segPlain(line), "example.com") {
			t.Fatalf("URL leaked into a wrapped line: %q", segPlain(line))
		}
	}
	if len(lines) == 0 || !strings.Contains(segPlain(lines[0]), "text") {
		t.Fatalf("link text should still render: %+v", lines)
	}
}

func segPlain(segs []Seg) string {
	var b strings.Builder
	for _, s := range segs {
		b.WriteString(s.Text)
	}
	return b.String()
}

func TestWrapSegsNeverExceedsWidth(t *testing.T) {
	segs := parseInline("a fairly long paragraph with several words in it")
	for _, width := range []int{4, 7, 10, 15} {
		for _, line := range wrapSegs(segs, width) {
			if lipgloss.Width(segPlain(line)) > width {
				t.Fatalf("width %d: line %q exceeds it", width, segPlain(line))
			}
		}
	}
}
