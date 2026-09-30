package kanban

import (
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// docs/fleet.md is the contract for acceptance gates. These tests fail when a
// gate kind, state or limit changes without the pages following.
func TestGatesAreDocumented(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/fleet.md")), " ")
	for _, want := range []string{
		"### Acceptance gates",
		"A gate is a reference, never a command",
		"A bad gate refuses the whole handoff",
		"A worker sees its card's gates",
		"`kanban.gates.require`",
		"starts every gate `unmet`",
		fmt.Sprintf("at most %d characters", MaxGateTitleRunes),
		"at most eight a card",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/fleet.md lacks %q", want)
		}
	}
	if MaxGates != 8 {
		t.Errorf("MaxGates is %d but the docs say eight", MaxGates)
	}
	for _, k := range []GateKind{GateRunnable, GateManual} {
		if !strings.Contains(doc, "`"+string(k)+"`") {
			t.Errorf("docs/fleet.md does not name the %q kind", k)
		}
	}
}

func TestGateBoardVersionIsDocumented(t *testing.T) {
	doc := docparity.Read(t, "docs/bkan.md")
	want := fmt.Sprintf("00 %02d", formatVersion)
	if !strings.Contains(doc, "currently "+want) {
		t.Errorf("docs/bkan.md does not state the current board version %s", want)
	}
	if !strings.Contains(doc, fmt.Sprintf("this Belai reads %d to %d", minVersion, formatVersion)) {
		t.Errorf("docs/bkan.md does not state the readable versions %d to %d", minVersion, formatVersion)
	}
	if !strings.Contains(doc, "| `Gates` |") {
		t.Error("docs/bkan.md does not describe the Gates field")
	}
}

func TestKanbanPageStatesTheGateLimits(t *testing.T) {
	doc := docparity.Read(t, "docs/kanban.md")
	want := fmt.Sprintf("at most %d acceptance gates, each title at most %d characters", MaxGates, MaxGateTitleRunes)
	if !strings.Contains(doc, want) {
		t.Errorf("docs/kanban.md lacks %q", want)
	}
}
