package components

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// mdPlain renders a row's segments to their unstyled text.
func mdPlain(r Row) string {
	var b strings.Builder
	for _, s := range r.Segs {
		b.WriteString(s.Text)
	}
	return b.String()
}

// assertMDInvariants checks the contracts every markdown render must hold: one
// source entry per row, no escape bytes or newlines inside any segment, and no
// rendered line wider than the frame.
func assertMDInvariants(t *testing.T, src string, width int, md MD) {
	t.Helper()
	if len(md.Rows) != len(md.Src) {
		t.Fatalf("%q: %d rows vs %d src entries", src, len(md.Rows), len(md.Src))
	}
	for i, r := range md.Rows {
		if md.Src[i] < 0 || md.Src[i] >= len(strings.Split(src, "\n")) {
			t.Fatalf("%q: row %d src %d out of range", src, i, md.Src[i])
		}
		line, sl := r.Render(width)
		if lipgloss.Width(line) > width {
			t.Fatalf("%q: row %d renders %d cells > %d: %q", src, i, lipgloss.Width(line), width, line)
		}
		if strings.Contains(line, "\n") {
			t.Fatalf("%q: row %d emitted a newline", src, i)
		}
		for _, s := range r.Segs {
			if strings.ContainsRune(s.Text, 0x1b) || strings.ContainsRune(s.Text, '\n') {
				t.Fatalf("%q: row %d segment carries escape or newline: %q", src, i, s.Text)
			}
		}
		if !sl.Chrome {
			if got := ansi.Cut(ansi.Strip(line), sl.Col, sl.Col+sl.Width); got != sl.Text {
				t.Fatalf("%q: row %d invariant violated: Cut=%q Text=%q", src, i, got, sl.Text)
			}
		}
	}
}

// TestRenderMarkdownInvariants drives the whole renderer over a corpus of
// inputs and widths, asserting the structural contracts above.
func TestRenderMarkdownInvariants(t *testing.T) {
	corpus := []string{
		"plain paragraph",
		"# heading one\n\ntext",
		"### heading three",
		"a paragraph that is long enough to wrap onto more than one line at a narrow width",
		"- one\n- two\n- three",
		"1. first\n2. second\n3. third",
		"- parent\n  - child\n  - child two\n- parent two",
		"- [ ] todo\n- [x] done",
		"> a quote\n> spanning two lines",
		"```go\nfunc main() {}\n```",
		"```\nunterminated fence\nstill code",
		"---",
		"**bold** and *em* and `code`",
		"| a | b |\n|---|---|\n| 1 | 2 |",
		"paragraph\n\n| a | b |\n|---|---|\n| 1 | 2 |",
		"half-typed table\n| a | b |\n|---|---|",
		"***both*** ~~strike~~ [link](https://example.com)",
		"    indented code block\n    line two",
	}
	for _, src := range corpus {
		for _, width := range []int{20, 40, 60, 80} {
			md := RenderMarkdown(src, width)
			assertMDInvariants(t, src, width, md)
		}
	}
}

func TestRenderMarkdownHeadingLevels(t *testing.T) {
	md := RenderMarkdown("# Big\n\n### Small", 80)
	if len(md.Rows) != 2 {
		t.Fatalf("want 2 heading rows, got %d", len(md.Rows))
	}
	if got := mdPlain(md.Rows[0]); got != "Big" {
		t.Fatalf("h1 text = %q", got)
	}
	if md.Rows[0].Segs[0].FG != ColorCream {
		t.Fatalf("h1 should be cream, got %v", md.Rows[0].Segs[0].FG)
	}
	if md.Rows[1].Segs[0].FG != ColorTealSoft {
		t.Fatalf("h3 should be teal-soft, got %v", md.Rows[1].Segs[0].FG)
	}
}

func TestRenderMarkdownParagraphWraps(t *testing.T) {
	md := RenderMarkdown("one two three four five six seven eight nine ten", 20)
	if len(md.Rows) < 2 {
		t.Fatalf("want wrapped paragraph, got %d rows: %q", len(md.Rows), rowsText(md.Rows))
	}
	for _, r := range md.Rows {
		if lipgloss.Width(mdPlain(r)) > 20 {
			t.Fatalf("wrapped line exceeds width: %q", mdPlain(r))
		}
	}
}

func TestRenderMarkdownHardBreak(t *testing.T) {
	md := RenderMarkdown("first line  \nsecond line", 80)
	if len(md.Rows) != 2 {
		t.Fatalf("hard break should yield two rows, got %d: %q", len(md.Rows), rowsText(md.Rows))
	}
	if mdPlain(md.Rows[0]) != "first line" || mdPlain(md.Rows[1]) != "second line" {
		t.Fatalf("hard break rows = %q", rowsText(md.Rows))
	}
}

func TestRenderMarkdownListMarkers(t *testing.T) {
	md := RenderMarkdown("- [ ] todo\n- [x] done\n- plain", 40)
	if len(md.Rows) != 3 {
		t.Fatalf("want 3 list rows, got %d: %q", len(md.Rows), rowsText(md.Rows))
	}
	if !strings.Contains(mdPlain(md.Rows[0]), "☐") || !strings.Contains(mdPlain(md.Rows[1]), "☑") {
		t.Fatalf("task markers missing: %q", rowsText(md.Rows))
	}
	if !strings.Contains(mdPlain(md.Rows[2]), "•") {
		t.Fatalf("bullet marker missing: %q", rowsText(md.Rows))
	}
}

func TestRenderMarkdownOrderedRenumbers(t *testing.T) {
	md := RenderMarkdown("9. nine\n1. one\n3. three", 40)
	got := strings.Join(rowsText(md.Rows), "\n")
	if !strings.Contains(got, "1. nine") || !strings.Contains(got, "2. one") || !strings.Contains(got, "3. three") {
		t.Fatalf("ordered items not renumbered: %q", got)
	}
}

func TestRenderMarkdownNestedListIndents(t *testing.T) {
	md := RenderMarkdown("- parent\n  - child\n- parent two", 40)
	got := strings.Join(rowsText(md.Rows), "\n")
	if !strings.Contains(got, "• parent") || !strings.Contains(got, "◦ child") {
		t.Fatalf("nested list markers wrong: %q", got)
	}
	// The child must be indented further than the parent.
	var parentCol, childCol int
	for _, r := range md.Rows {
		if mdPlain(r) == "• parent" {
			parentCol = r.Gutter
		}
		if strings.Contains(mdPlain(r), "◦ child") {
			childCol = r.Gutter
		}
	}
	if childCol <= parentCol {
		t.Fatalf("child gutter %d not deeper than parent %d", childCol, parentCol)
	}
}

func TestRenderMarkdownBlockquote(t *testing.T) {
	md := RenderMarkdown("> quoted", 40)
	if len(md.Rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(md.Rows))
	}
	if !strings.Contains(mdPlain(md.Rows[0]), "quoted") {
		t.Fatalf("quote text missing: %q", mdPlain(md.Rows[0]))
	}
	if md.Rows[0].Segs[0].Text != "▏ " || md.Rows[0].Segs[0].FG != ColorMuted {
		t.Fatalf("quote gutter missing: %+v", md.Rows[0].Segs[0])
	}
}

func TestRenderMarkdownUnterminatedFence(t *testing.T) {
	md := RenderMarkdown("```go\nfunc main() {}\nfmt.Println(1)", 40)
	got := strings.Join(rowsText(md.Rows), "\n")
	if !strings.Contains(got, "func main() {}") || !strings.Contains(got, "fmt.Println(1)") {
		t.Fatalf("unterminated fence lost content: %q", got)
	}
}

func TestRenderMarkdownFenceHighlighted(t *testing.T) {
	withProfile(t, termenv.TrueColor, func() {
		md := RenderMarkdown("```go\nfunc main() {}", 40)
		var colored bool
		for _, r := range md.Rows {
			for _, s := range r.Segs {
				if s.FG != nil {
					colored = true
				}
			}
		}
		if !colored {
			t.Fatalf("go fence should highlight: %+v", md.Rows)
		}
	})
}

func TestRenderMarkdownThematicBreak(t *testing.T) {
	md := RenderMarkdown("---", 30)
	if len(md.Rows) != 1 {
		t.Fatalf("want 1 rule row, got %d", len(md.Rows))
	}
	if strings.Trim(mdPlain(md.Rows[0]), "─") != "" {
		t.Fatalf("rule row is not a hairline: %q", mdPlain(md.Rows[0]))
	}
}

// TestTruncateMarkdownRecoversRawRemainder pins the row-level truncation: the
// hint's Hidden is the original markdown source from the first dropped row's
// source line, so a selection over the hint copies the raw remainder.
func TestTruncateMarkdownRecoversRawRemainder(t *testing.T) {
	src := "# head\n\npara one\n\npara two\n\npara three\n\npara four\n\npara five"
	md := RenderMarkdown(src, 40)
	rows, marker, hidden := truncateMarkdown(src, md, assistantPreviewLines)
	if len(rows) != assistantPreviewLines+1 {
		t.Fatalf("want %d kept rows + marker, got %d", assistantPreviewLines, len(rows))
	}
	if marker != "… 2 more lines" {
		t.Fatalf("marker = %q", marker)
	}
	// The source has five paragraphs after the heading; four rows are shown,
	// so the hidden remainder is the last two source lines.
	if !strings.Contains(hidden, "para four") || !strings.Contains(hidden, "para five") {
		t.Fatalf("hidden = %q", hidden)
	}
	if strings.Contains(hidden, "para one") {
		t.Fatalf("hidden should not contain already-shown source: %q", hidden)
	}
}

func rowsText(rows []Row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = mdPlain(r)
	}
	return out
}
