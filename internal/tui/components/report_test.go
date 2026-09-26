package components

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// A report card renders its markdown body under its title and collapses a
// long report to a preview until expanded.
func TestReportPanelRendersAndCollapses(t *testing.T) {
	var body []string
	for i := 0; i < 30; i++ {
		body = append(body, "- finding "+strings.Repeat("x", i%5))
	}
	msg := Message{Role: ReportRole, ToolName: "vulnetix sast review", ToolArgs: "42s", Content: strings.Join(body, "\n")}
	collapsed, _ := reportPanel(msg, 80, false)
	expanded, _ := reportPanel(msg, 80, true)
	plain := ansi.Strip(collapsed)
	if !strings.Contains(plain, "vulnetix sast review") || !strings.Contains(plain, "42s") {
		t.Fatalf("title or meta missing:\n%s", plain)
	}
	if strings.Count(collapsed, "\n") >= strings.Count(expanded, "\n") {
		t.Fatal("a long report must collapse to a preview")
	}
	for _, line := range strings.Split(expanded, "\n") {
		if w := lipgloss.Width(line); w > 80 {
			t.Fatalf("line wider than the panel (%d): %q", w, ansi.Strip(line))
		}
	}
}

// The running review leads the roster line at every width without wrapping,
// and the footer keeps its fixed height.
func TestFooterReviewLineFitsTheWidth(t *testing.T) {
	r := &ReviewProgress{Glyph: "●", Done: 6, Total: 10, Pending: []string{"secrets", "containers", "aibom", "cbom"}, Agents: 2, Elapsed: "3m12s"}
	for _, chips := range [][]SubagentChip{nil, {{ID: "bg:x", Label: "signet:vulnetix-scanner@sast#1", State: "running"}}} {
		for w := 30; w <= 140; w += 10 {
			f := Footer{Width: w, Review: r, Subagents: chips}
			line := f.subagentLine()
			if got := lipgloss.Width(line); got > w {
				t.Fatalf("width %d: review line is %d wide: %q", w, got, ansi.Strip(line))
			}
			if !strings.Contains(ansi.Strip(line), "vulnetix review") {
				t.Fatalf("width %d: review missing: %q", w, ansi.Strip(line))
			}
			without := Footer{Width: w, Subagents: chips}
			if strings.Count(f.View(), "\n") != strings.Count(without.View(), "\n") {
				t.Fatalf("width %d: the review changed the footer height", w)
			}
		}
	}
	wide := ansi.Strip((&Footer{Width: 140, Review: r}).subagentLine())
	for _, want := range []string{"6/10", "secrets, containers, aibom +1", "2 agents", "3m12s"} {
		if !strings.Contains(wide, want) {
			t.Fatalf("wide review line lacks %q: %q", want, wide)
		}
	}
}

// The frame says how the activity went: failed in the danger accent, a
// result that needs attention in the warning accent.
func TestReportPanelAccentFollowsStatus(t *testing.T) {
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(old)
	plain, _ := reportPanel(Message{Role: ReportRole, ToolName: "vulnetix cbom", Content: "- ok"}, 60, false)
	attention, _ := reportPanel(Message{Role: ReportRole, ToolName: "vulnetix cbom", Content: "- ok", Status: ReportAttention}, 60, false)
	failed, _ := reportPanel(Message{Role: ReportRole, ToolName: "vulnetix cbom", Content: "- ok", Status: ReportFailed}, 60, false)
	if plain == attention || attention == failed || plain == failed {
		t.Fatal("each status must draw its own accent")
	}
}
