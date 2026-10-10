package main

import (
	"strings"
	"testing"
)

func TestPlanReviewHintOffersApproveRefineAndCancel(t *testing.T) {
	got := planReviewHint("/work/proj/.vulnetix/plans/plan-x.md", "/work/proj")
	for _, want := range []string{
		"plan written: /work/proj/.vulnetix/plans/plan-x.md\n",
		`approve: belai -allow-ask-without-tty -prompt "@.vulnetix/plans/plan-x.md"`,
		`refine:  belai -plan -prompt "Revise @.vulnetix/plans/plan-x.md: <what to change>"`,
		"cancel:",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("hint missing %q:\n%s", want, got)
		}
	}
	// A plan outside the working directory keeps its absolute path.
	if got := planReviewHint("/elsewhere/p.md", "/work/proj"); !strings.Contains(got, `"@/elsewhere/p.md"`) {
		t.Errorf("outside plan should stay absolute:\n%s", got)
	}
}
