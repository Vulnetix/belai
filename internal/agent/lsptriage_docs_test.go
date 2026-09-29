package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
	"github.com/vulnetix/belai/internal/rolemanager/jev"
)

// TestJevJobsPageStatesTheTriageConstants keeps the LSP triage section of
// docs/jev-jobs.md equal to the constants the code uses.
func TestJevJobsPageStatesTheTriageConstants(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/jev-jobs.md")), " ")
	for _, want := range []string{
		"From the second pass on",
		fmt.Sprintf("a score below %.2f", jev.TriageAt),
		fmt.Sprintf("(default %d, from 2 to 20)", defaultRepairAttempts),
		fmt.Sprintf("up to %d errors", triageMaxRows),
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/jev-jobs.md does not state %q", want)
		}
	}
	if triageMinAttempt != 2 || defaultRepairAttempts != 4 || triageMaxRows != 10 || jev.TriageAt != 0.30 {
		t.Error("a triage constant changed: update docs/jev-jobs.md and this test")
	}
}
