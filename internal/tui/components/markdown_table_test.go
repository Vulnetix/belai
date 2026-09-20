package components

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// TestRenderMarkdownTableBoxed checks that a GFM table renders as a boxed grid
// with its header, separator and data rows present.
func TestRenderMarkdownTableBoxed(t *testing.T) {
	md := RenderMarkdown("| name | count |\n|:-----|------:|\n| a    | 1     |\n| bb   | 22    |", 40)
	got := rowsText(md.Rows)
	joined := strings.Join(got, "\n")
	for _, want := range []string{"name", "count", "a", "1", "bb", "22", "┌", "├", "└", "│"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("boxed table missing %q:\n%s", want, joined)
		}
	}
	// A boxed table has a top border, header, separator, data rows, bottom.
	if len(got) < 5 {
		t.Fatalf("want at least 5 lines (borders + header + rows), got %d:\n%s", len(got), joined)
	}
}

// TestRenderMarkdownTableAlignment checks that a right-aligned column pads a
// narrow cell on the left so it sits under the header's right edge.
func TestRenderMarkdownTableAlignment(t *testing.T) {
	md := RenderMarkdown("| head |\n|-----:|\n| x |", 16)
	got := rowsText(md.Rows)
	if len(got) < 5 {
		t.Fatalf("want boxed table, got %q", got)
	}
	// Right-aligned data row: three leading spaces pad the one-cell content.
	if got[3] != "│   x│" {
		t.Fatalf("right-aligned data row = %q, want %q", got[3], "│   x│")
	}
}

// TestRenderMarkdownTableOverwideShrinks checks that a table wider than the
// frame shrinks and wraps instead of exceeding the width.
func TestRenderMarkdownTableOverwideShrinks(t *testing.T) {
	md := RenderMarkdown("| aaaaaaaaaa | bbbbbbbbbb |\n|---|---|\n| cccccccccc | dddddddddd |", 20)
	for _, r := range md.Rows {
		if lipgloss.Width(mdPlain(r)) > 20 {
			t.Fatalf("table row exceeds width: %q", mdPlain(r))
		}
	}
}

// TestRenderMarkdownTableFallsBackToPlain checks the narrow fallback: below the
// minimum the table renders as plain " | "-joined rows, not a box.
func TestRenderMarkdownTableFallsBackToPlain(t *testing.T) {
	md := RenderMarkdown("| aaaaa | bbbbb | ccccc |\n|---|---|---|\n| 11111 | 22222 | 33333 |", 12)
	joined := strings.Join(rowsText(md.Rows), "\n")
	if strings.Contains(joined, "┌") {
		t.Fatalf("narrow table should not box, got:\n%s", joined)
	}
	if !strings.Contains(joined, "aaaaa") || !strings.Contains(joined, "33333") {
		t.Fatalf("plain fallback missing cells:\n%s", joined)
	}
}

// TestRenderMarkdownTableHalfTyped pins the streaming rule: a header followed
// by a delimiter row with no data renders instead of panicking or vanishing.
func TestRenderMarkdownTableHalfTyped(t *testing.T) {
	md := RenderMarkdown("| a | b |\n|---|---|", 40)
	if len(md.Rows) == 0 {
		t.Fatal("half-typed table rendered nothing")
	}
	joined := strings.Join(rowsText(md.Rows), "\n")
	if !strings.Contains(joined, "a") || !strings.Contains(joined, "b") {
		t.Fatalf("half-typed table lost header:\n%s", joined)
	}
}

// TestSplitTableRowEscapedPipe checks that \| is a literal pipe, not a column
// separator.
func TestSplitTableRowEscapedPipe(t *testing.T) {
	cells := splitTableRow(`| a\|b | c |`)
	if len(cells) != 2 || cells[0] != "a|b" || cells[1] != "c" {
		t.Fatalf("cells = %q", cells)
	}
}

// TestMarkdownTableInvariants checks the full boxed and plain paths hold the
// row invariants across widths and profiles.
func TestMarkdownTableInvariants(t *testing.T) {
	src := "| name | value |\n|:-----|------:|\n| alpha | 10 |\n| beta | 200000 |"
	for _, width := range []int{16, 24, 40, 80} {
		md := RenderMarkdown(src, width)
		assertMDInvariants(t, src, width, md)
	}
}
