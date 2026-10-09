package prompt

import (
	"strings"
	"testing"
)

// The decision-tools section appears only when the session has the tools, says
// what they are, when to use them and how to read the answer, and stays short.
func TestDecisionToolsSectionIsOnlyForASessionThatHasThem(t *testing.T) {
	without, err := System(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(without, "mcp__clef__") {
		t.Fatal("a session without the decision tools was told about them")
	}
	with, err := System(Options{DecisionTools: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Decision tools (mcp__clef__*)", "What:", "When:", "Why:", "How:", "Not for:", "decide_boolean", "gate_decision", "decide_enum", "rank_options", "decide_batch", "uncertain"} {
		if !strings.Contains(with, want) {
			t.Errorf("the section lacks %q", want)
		}
	}
	if n := len(decisionContract()); n > 2200 {
		t.Errorf("the section is %d bytes; keep it short", n)
	}
}

func TestDecisionToolsSectionIsNotGivenToAnExploreSubagent(t *testing.T) {
	got, err := System(Options{DecisionTools: true, Explore: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "Decision tools (mcp__clef__*)") {
		t.Fatal("an explore subagent was given the section")
	}
}
