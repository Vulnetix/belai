package docsuite

import (
	"path/filepath"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestSessionAssessmentNamesRealSymbols keeps the code names the assessment
// uses to describe its fixes pointing at code that exists.
func TestSessionAssessmentNamesRealSymbols(t *testing.T) {
	root := docparity.Root(t)
	for dir, names := range map[string][]string{
		"internal/agent": {"turnReadOnly", "spent", "IsContinuation", "PriorGoal", "toolConcurrency"},
		"internal/tools": {"ReadOnlySurface"},
		"internal/modes": {"RoutePlanOption"},
		"internal/goals": {"NewGoalState"},
	} {
		got := declared(t, filepath.Join(root, dir))
		for _, n := range names {
			if !got[n] {
				t.Errorf("%s declares no %s, which docs/session-assessment.md names", dir, n)
			}
		}
	}
}
