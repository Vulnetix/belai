package components

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// mixedTranscript covers every renderer: a user turn, a truncated assistant
// turn (marker + hidden remainder), a reasoning panel, an errored multi-line
// tool row (inline marker) and a system row.
func mixedTranscript() MessageList {
	return MessageList{
		Width:         80,
		ShowReasoning: true,
		ShowTools:     true,
		Messages: []Message{
			{Role: "user", Content: "fix the failing test in the wire package"},
			{Role: "assistant", Content: "Plan:\n1. reproduce\n2. patch\n3. verify\n4. ship\n5. tag\n6. announce"},
			{Role: "reasoning", Content: "thinking about the root cause here"},
			{Role: "tool", ToolName: "Bash", ToolArgs: `{"command":"go test ./..."}`,
				Content: "tool result withheld: provider error\nsecond line\nthird line\nfourth line", Status: "withheld"},
			{Role: "system", Content: "retrying (2/3) after 800ms — rate limited"},
		},
	}
}

// TestMessageListRenderLineMapMatchesRender pins the contract the whole
// selection feature rests on, over a mixed transcript at several widths:
// one map entry per rendered line, the invariant per selectable line, no
// border glyphs or ANSI in Text, and the marker line carrying its hidden
// remainder.
func TestMessageListRenderLineMapMatchesRender(t *testing.T) {
	for _, width := range []int{40, 80, 120} {
		ml := mixedTranscript()
		ml.Width = width
		rendered, lm := ml.Render()
		lines := strings.Split(rendered, "\n")

		if len(lm) != len(lines) {
			t.Fatalf("width %d: map %d entries vs %d rendered lines", width, len(lm), len(lines))
		}
		if len(lm) != lipgloss.Height(rendered) {
			t.Fatalf("width %d: map %d != height %d", width, len(lm), lipgloss.Height(rendered))
		}
		if ml.View() != rendered {
			t.Fatalf("width %d: View() and Render() diverge", width)
		}

		var markerCount int
		for i, sl := range lm {
			if sl.Chrome {
				continue
			}
			got := ansi.Cut(ansi.Strip(lines[i]), sl.Col, sl.Col+sl.Width)
			if got != sl.Text {
				t.Fatalf("width %d line %d: invariant violated: Cut=%q Text=%q (line=%q)",
					width, i, got, sl.Text, lines[i])
			}
			for _, bad := range []string{"│", "╭", "╮", "╰", "╯", "⌁"} {
				if strings.Contains(sl.Text, bad) {
					t.Fatalf("width %d line %d: decoration %q in Text: %q", width, i, bad, sl.Text)
				}
			}
			if strings.ContainsRune(sl.Text, 0x1b) {
				t.Fatalf("width %d line %d: ANSI in Text: %q", width, i, sl.Text)
			}
			if sl.MarkerWidth > 0 {
				markerCount++
				if sl.Hidden == "" {
					t.Fatalf("width %d line %d: marker without hidden text", width, i)
				}
			}
		}
		// The truncated assistant turn and the multi-line tool row must both
		// carry a marker at every width.
		if markerCount < 2 {
			t.Fatalf("width %d: want at least 2 marker lines (truncated turn, tool row), got %d", width, markerCount)
		}
	}
}

// TestMessageListCopyIsCleanEndToEnd copies whole and partial selections out
// of the mixed transcript and asserts the copy is the clean underlying text:
// no borders, no ⌁ prefix, no status words, and the truncation markers
// expanded to their hidden remainder.
func TestMessageListCopyIsCleanEndToEnd(t *testing.T) {
	ml := mixedTranscript()
	_, lm := ml.Render()

	// A "select everything" range ends one cell past the right edge of the
	// last line; per-line clamping keeps it in bounds.
	full := lm.Text(Pos{0, 0}, Pos{len(lm) - 1, 10_000})
	for _, bad := range []string{"│", "╭", "╮", "╰", "╯", "⌁", "more lines", "✓"} {
		if strings.Contains(full, bad) {
			t.Fatalf("full copy contains %q:\n%s", bad, full)
		}
	}
	// The truncated assistant turn's hidden remainder must be expanded.
	if !strings.Contains(full, "5. tag") || !strings.Contains(full, "6. announce") {
		t.Fatalf("hidden assistant lines not expanded:\n%s", full)
	}
	// The tool row's hidden remainder must be expanded, and its status word
	// and prefix must not be present.
	if !strings.Contains(full, "third line") || !strings.Contains(full, "fourth line") {
		t.Fatalf("hidden tool lines not expanded:\n%s", full)
	}

	// A single-line selection over the user turn copies exactly its text.
	userLine := 1 // the user panel's first body line (top edge is line 0)
	if userLine >= len(lm) || lm[userLine].Chrome {
		t.Fatalf("unexpected map shape: %+v", lm)
	}
	got := lm.Text(Pos{userLine, lm[userLine].Col}, Pos{userLine, lm[userLine].Col + lm[userLine].Width})
	if got != "fix the failing test in the wire package" {
		t.Fatalf("user line copy = %q", got)
	}
}

// TestMessageListExpandAllChangesNoMarkers checks that expansion (ctrl+o)
// renders the full content with no markers, so a copy after expand is the
// complete text.
func TestMessageListExpandAllChangesNoMarkers(t *testing.T) {
	ml := mixedTranscript()
	ml.ExpandAll = true
	_, lm := ml.Render()
	for i, sl := range lm {
		if sl.MarkerWidth > 0 {
			t.Fatalf("expanded render still has a marker on line %d: %+v", i, sl)
		}
	}
}
