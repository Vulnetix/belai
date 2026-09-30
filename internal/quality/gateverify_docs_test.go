package quality

import (
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// docs/fleet.md states how a gate is verified and what is kept; these tests
// fail when the code and the page disagree.
func TestVerificationIsDocumented(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/fleet.md")), " ")
	for _, want := range []string{
		"#### Verification",
		"`.vulnetix/belai/quality/verify/<card>-<commit>.json`",
		fmt.Sprintf("The last %d are kept", keepVerifications),
		"a run that tested nothing is not a pass",
		"go test -count=1 ./DIR",
		"`-run ^NAME$`",
		"No test output text reaches a card",
		"a note names at most five tests",
		fmt.Sprintf("at most %d characters", maxIdentLen),
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/fleet.md lacks %q", want)
		}
	}
	if VerifyDir != Dir+"/verify" || !strings.Contains(doc, "`"+strings.TrimSuffix(VerifyDir, "/verify")+"/verify/") {
		t.Errorf("docs/fleet.md does not give the record directory %s", VerifyDir)
	}
}

func TestVerificationInvariantIsInAgents(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "AGENTS.md")), " ")
	for _, want := range []string{"quality.GateArgv", "quality.JudgeGate", "Acceptance gates are references", "no custom oracle command now or later"} {
		if !strings.Contains(doc, want) {
			t.Errorf("AGENTS.md lacks %q", want)
		}
	}
}
