package components

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// When the details do not fit, the chips stay and the details go, before any
// chip collapses into the overflow marker.
func TestChipDetailsDropBeforeChips(t *testing.T) {
	f := Footer{Width: 30, Subagents: []SubagentChip{{ID: "e1", Label: "explore 1", State: "running", Glyph: "●", Detail: "a-very-long-tool-name"}}}
	line := ansi.Strip(f.subagentLine())
	if !strings.Contains(line, "explore 1") || strings.Contains(line, "a-very-long") || strings.Contains(line, "more") {
		t.Fatalf("narrow roster = %q", line)
	}
	f.Width = 80
	if line := ansi.Strip(f.subagentLine()); !strings.Contains(line, "a-very-long-tool-name") {
		t.Fatalf("wide roster lost the detail: %q", line)
	}
}

// The pulse keeps its counters and gives up the latest output first, and it
// never spills past the footer width.
func TestPulseLineFitsTheWidth(t *testing.T) {
	p := &AgentPulse{Glyph: "●", Label: "triage", State: "running", Step: "⚙ Read", Iter: "iter 2/12", Tools: 3, Elapsed: "41s", Last: strings.Repeat("output ", 40)}
	for _, w := range []int{40, 80, 140} {
		f := Footer{Width: w, Pulse: p}
		line := f.subagentLine()
		if got := ansi.StringWidth(line); got > w {
			t.Fatalf("width %d: pulse is %d cells", w, got)
		}
		if !strings.Contains(ansi.Strip(line), "triage") {
			t.Fatalf("width %d: pulse lost its label: %q", w, ansi.Strip(line))
		}
	}
	wide := ansi.Strip((&Footer{Width: 140, Pulse: p}).subagentLine())
	for _, want := range []string{"⚙ Read", "iter 2/12", "3 tools", "41s", "› output", "esc main"} {
		if !strings.Contains(wide, want) {
			t.Errorf("wide pulse is missing %q: %q", want, wide)
		}
	}
}
