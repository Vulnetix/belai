package fleet

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/docparity"
)

func TestFleetDocDescribesGateVerification(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/fleet.md")), " ")
	for _, mode := range []string{agentprofile.VerifyOff, agentprofile.VerifyRecord, agentprofile.VerifyEnforce} {
		if !strings.Contains(doc, "| `"+mode+"`") {
			t.Errorf("docs/fleet.md does not tabulate the %q verify mode", mode)
		}
	}
	for _, want := range []string{
		"A gate that cannot run blocks the card",
		"A suite that is gone is unmet, not blocked",
		"A manual gate is never run",
		"Nothing to verify passes",
		"Identical commands run once",
		"the lease keeps renewing while the suites run",
		"With no base record nothing is compared",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/fleet.md lacks %q", want)
		}
	}
}

func TestBuiltinBuilderAndReviewerEnforceGates(t *testing.T) {
	for _, name := range []string{"belai:builder", "belai:reviewer"} {
		p, err := agentprofile.Load(name)
		if err != nil {
			t.Fatal(err)
		}
		if p.Kanban.Gates.VerifyMode() != agentprofile.VerifyEnforce {
			t.Errorf("%s must enforce gate verification, has %q", name, p.Kanban.Gates.VerifyMode())
		}
	}
	doc := docparity.Read(t, "docs/agent-profiles.md")
	if !strings.Contains(doc, "once the harness has verified the card's gates on the branch") {
		t.Error("docs/agent-profiles.md does not say the builder's hand-on waits for verification")
	}
}

func TestCoverageLabelAndSettingAreDocumented(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/fleet.md")), " ")
	if !strings.Contains(doc, "`"+CoverageLabel+"` label") {
		t.Errorf("docs/fleet.md does not name the %q label", CoverageLabel)
	}
	if !strings.Contains(doc, "`kanban.gates.coverage`") {
		t.Error("docs/fleet.md does not name kanban.gates.coverage")
	}
	agents := docparity.Read(t, "docs/agent-profiles.md")
	if !strings.Contains(agents, "`kanban.gates.coverage`") {
		t.Error("docs/agent-profiles.md does not name kanban.gates.coverage")
	}
}
