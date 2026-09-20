package components

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
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

// renderProfiles are the colour profiles every render-invariant assertion must
// hold under. Without a TTY lipgloss degrades every style to plain text, so an
// Ascii-only run asserts nothing at all about ANSI handling — the "no ANSI in
// Text" and cell-width checks below only have teeth under TrueColor.
var renderProfiles = []termenv.Profile{termenv.Ascii, termenv.TrueColor}

// profileName gives a colour profile a readable name for subtest output.
func profileName(p termenv.Profile) string {
	switch p {
	case termenv.TrueColor:
		return "truecolor"
	case termenv.ANSI256:
		return "ansi256"
	case termenv.ANSI:
		return "ansi"
	default:
		return "ascii"
	}
}

// withProfile runs fn with the given colour profile installed, restoring the
// previous one afterwards.
func withProfile(t *testing.T, p termenv.Profile, fn func()) {
	t.Helper()
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(p)
	defer lipgloss.SetColorProfile(old)
	fn()
}

// assertLineMapInvariant checks every contract the selection feature rests on
// for one rendered frame: one map entry per rendered line, the documented
// Cut/Strip invariant per selectable line, no decoration or ANSI in Text, no
// line wider than the frame, and markers carrying their hidden remainder. It
// returns the number of marker lines it saw.
func assertLineMapInvariant(t *testing.T, label, rendered string, lm LineMap, width int) int {
	t.Helper()
	lines := strings.Split(rendered, "\n")

	if len(lm) != len(lines) {
		t.Fatalf("%s: map %d entries vs %d rendered lines", label, len(lm), len(lines))
	}
	if len(lm) != lipgloss.Height(rendered) {
		t.Fatalf("%s: map %d != height %d", label, len(lm), lipgloss.Height(rendered))
	}

	var markerCount int
	for i, sl := range lm {
		if w := lipgloss.Width(lines[i]); w > width {
			t.Fatalf("%s line %d: rendered width %d exceeds frame width %d: %q",
				label, i, w, width, lines[i])
		}
		if sl.Chrome {
			continue
		}
		got := ansi.Cut(ansi.Strip(lines[i]), sl.Col, sl.Col+sl.Width)
		if got != sl.Text {
			t.Fatalf("%s line %d: invariant violated: Cut=%q Text=%q (line=%q)",
				label, i, got, sl.Text, lines[i])
		}
		for _, bad := range []string{"│", "╭", "╮", "╰", "╯", "⌁"} {
			if strings.Contains(sl.Text, bad) {
				t.Fatalf("%s line %d: decoration %q in Text: %q", label, i, bad, sl.Text)
			}
		}
		if strings.ContainsRune(sl.Text, 0x1b) {
			t.Fatalf("%s line %d: ANSI in Text: %q", label, i, sl.Text)
		}
		if sl.MarkerWidth > 0 {
			markerCount++
			if sl.Hidden == "" {
				t.Fatalf("%s line %d: marker without hidden text", label, i)
			}
		}
	}
	return markerCount
}

// TestMessageListRenderLineMapMatchesRender pins the contract the whole
// selection feature rests on, over a mixed transcript at every width the
// transcript can be asked to render at, under both colour profiles.
func TestMessageListRenderLineMapMatchesRender(t *testing.T) {
	for _, profile := range renderProfiles {
		for width := 30; width <= 140; width++ {
			name := fmt.Sprintf("%s/w%03d", profileName(profile), width)
			t.Run(name, func(t *testing.T) {
				withProfile(t, profile, func() {
					ml := mixedTranscript()
					ml.Width = width
					rendered, lm := ml.Render()

					// Render clamps to messageMinWidth, so that — not the
					// requested width — is the frame lines must fit inside.
					frame := max(width, messageMinWidth)

					if ml.View() != rendered {
						t.Fatalf("View() and Render() diverge")
					}
					markerCount := assertLineMapInvariant(t, name, rendered, lm, frame)

					// The truncated assistant turn and the multi-line tool row
					// must both carry a marker at every width.
					if markerCount < 2 {
						t.Fatalf("want at least 2 marker lines (truncated turn, tool row), got %d",
							markerCount)
					}
				})
			})
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

// TestMessageListRenderTagsProvenance pins the per-line provenance hover
// hit-testing depends on: every line carries the index of the message that
// rendered it, the file-panel flag for Read results with a path, and the
// collapsed flag for truncated panels. Separator chrome carries Owner -1.
func TestMessageListRenderTagsProvenance(t *testing.T) {
	ml := MessageList{
		Width:     80,
		ShowTools: true,
		Messages: []Message{
			// Truncated turn: collapsed, not a file.
			{Role: "assistant", Content: "l1\nl2\nl3\nl4\nl5"},
			// Read with a path and more than the 3-line preview: file AND collapsed.
			{Role: "tool", ToolName: "Read", ToolArgs: `{"path":"main.go"}`,
				Meta:    map[string]any{"path": "main.go"},
				Content: "package main\n\nimport \"fmt\"\n\nfunc main() {}"},
			// Short Read with a path: file but not collapsed.
			{Role: "tool", ToolName: "Read", ToolArgs: `{"path":"a.md"}`,
				Meta: map[string]any{"path": "a.md"}, Content: "# hi\n"},
			// Errored Bash: not a file, not collapsed (single line result).
			{Role: "tool", ToolName: "Bash", ToolArgs: `{"command":"false"}`,
				Content: "tool result withheld: denied", Status: "withheld"},
			// System row: not a file, not collapsed.
			{Role: "system", Content: "done"},
		},
	}
	_, lm := ml.Render()

	ownerSeen := map[int]int{}
	separators := 0
	for i, sl := range lm {
		if sl.Owner < 0 {
			if !sl.Chrome {
				t.Fatalf("line %d has Owner -1 but is not chrome: %+v", i, sl)
			}
			separators++
			continue
		}
		if sl.Owner >= len(ml.Messages) {
			t.Fatalf("line %d owner %d out of range", i, sl.Owner)
		}
		ownerSeen[sl.Owner]++
		msg := ml.Messages[sl.Owner]
		wantFile := msg.Role == "tool" && msg.ToolName == "Read"
		if sl.File != wantFile {
			t.Fatalf("line %d (owner %d) File=%v, want %v", i, sl.Owner, sl.File, wantFile)
		}
		// Collapsed follows the truncation marker the renderer placed: the
		// 5-line Read is collapsed, everything else is not. The assistant turn
		// is rendered as a markdown panel and no longer carries the collapsed
		// provenance flag on its chrome line.
		wantCollapsed := sl.Owner == 1
		if sl.Collapsed != wantCollapsed {
			t.Fatalf("line %d (owner %d) Collapsed=%v, want %v", i, sl.Owner, sl.Collapsed, wantCollapsed)
		}
	}
	if separators == 0 {
		t.Fatal("expected at least one separator chrome line with Owner -1")
	}
	for i := range ml.Messages {
		if ownerSeen[i] == 0 {
			t.Fatalf("message %d rendered no lines", i)
		}
	}
	// Find a collapsed file-panel line to confirm the file+collapsed hint works.
	foundCollapsed := false
	for _, sl := range lm {
		if sl.Collapsed && sl.File {
			foundCollapsed = true
			break
		}
	}
	if !foundCollapsed {
		t.Fatal("expected at least one collapsed file-panel line")
	}
}
