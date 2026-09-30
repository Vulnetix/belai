package tools

import (
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

func normalised(t *testing.T, rel string) string {
	t.Helper()
	return strings.Join(strings.Fields(docparity.Read(t, rel)), " ")
}

// docs/fleet.md states the clarity rule in numbers; the numbers are code.
func TestClarityRuleIsDocumented(t *testing.T) {
	doc := normalised(t, "docs/fleet.md")
	for _, want := range []string{
		fmt.Sprintf("the title is at most %d characters", ClearTitleRunes),
		fmt.Sprintf("the body is %d to %d characters", ClearBodyMin, ClearBodyMax),
		fmt.Sprintf("it has at most %d gates", ClearMaxGates),
		"at least one runnable gate",
		"`needs_clarification` or `needs_split`",
		"declaring a task clear never moves it out of Review",
		"`review:` line appended to its body",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/fleet.md lacks %q", want)
		}
	}
	for _, c := range []string{ClarityClear, ClarityUnclear, ClaritySplit} {
		if !strings.Contains(doc, c) {
			t.Errorf("docs/fleet.md does not name the clarity %q", c)
		}
	}
}

func TestManualGatesAreDocumented(t *testing.T) {
	doc := normalised(t, "docs/fleet.md")
	for _, want := range []string{
		"#### Manual gates and the reviewer",
		"`KanbanGate`",
		"Each review decides afresh",
		"Abandonment is never success",
		"A reviewer cannot overrule the harness",
		"A builder decides no manual gate",
		fmt.Sprintf("at most %d characters", maxGateEvidence),
		"`HANDOFF REQUIRED`",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/fleet.md lacks %q", want)
		}
	}
	kdoc := normalised(t, "docs/kanban.md")
	if !strings.Contains(kdoc, "**`"+KanbanGateName+"`**") || !strings.Contains(kdoc, "refuses a runnable gate") {
		t.Errorf("docs/kanban.md does not describe %s", KanbanGateName)
	}
	agents := normalised(t, "AGENTS.md")
	for _, want := range []string{"`KanbanGate` is a reviewer's tool", "tools.ClearTitleRunes", "no declaration or score moves a card out of the human gate"} {
		if !strings.Contains(agents, want) {
			t.Errorf("AGENTS.md lacks %q", want)
		}
	}
}
