package agent

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/docparity"
)

// TestRoleManagerPageStatesTheLoopBounds pins the iteration and pass numbers in
// the Max-iteration bound and Plan pass loop sections of docs/role-manager.md.
func TestRoleManagerPageStatesTheLoopBounds(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/role-manager.md")), " ")
	for _, want := range []string{
		"The default maximum is 40 iterations (`resilience.max_iterations`)",
		"(0 falls back to `defaultAgentContinuations` = 5 in agent mode)",
		"(0 falls back to `defaultPlanContinuations` = 3)",
		"each plan pass reads for at most `planPassIterations` (12) tool rounds",
		"capped by `resilience.max_explore_iterations` (default 8)",
		"deduplicated, at most 30)",
		"Reaching `goalStallPartial` (`2 × goalVerifyEvery`)",
		"Reaching `goalNoWritePasses` (2) injects the no-write directive",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/role-manager.md does not say %q", want)
		}
	}
	if config.DefaultMaxIterations != 40 || defaultAgentContinuations != 5 || defaultPlanContinuations != 3 || planPassIterations != 12 || maxPlanReadPaths != 30 || goalStallPartial != 2*goalVerifyEvery || goalNoWritePasses != 2 {
		t.Errorf("bounds %d/%d/%d/%d disagree with the page", config.DefaultMaxIterations, defaultAgentContinuations, defaultPlanContinuations, planPassIterations)
	}
	// The explore cap is the task's own budget held by the setting; the default
	// the session passes is 8.
	if got := exploreBudget(5, 8); got != 5 {
		t.Errorf("exploreBudget(5, 8) = %d, want the task's own 5", got)
	}
	if got := exploreBudget(5, 3); got != 3 {
		t.Errorf("exploreBudget(5, 3) = %d, want the setting's 3 to cap it", got)
	}
}
