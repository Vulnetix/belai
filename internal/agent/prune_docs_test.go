package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestJevJobsPageStatesThePruneConstants keeps the compaction prune section of
// docs/jev-jobs.md equal to the constants the code uses.
func TestJevJobsPageStatesThePruneConstants(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/jev-jobs.md")), " ")
	for _, want := range []string{
		fmt.Sprintf("the newest %d turns", pruneKeepRecent),
		fmt.Sprintf("first %d characters", pruneHeadChars),
		fmt.Sprintf("more than %d characters longer", pruneTruncateSlack),
		fmt.Sprintf("last %s requests", "three"),
		fmt.Sprintf("about %s for the local model", "7,000"),
		fmt.Sprintf("%s for a remote backend", "24,000"),
		"1,000, then 200, then 60",
		"70 percent",
		"at or above 0.50",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/jev-jobs.md does not state %q", want)
		}
	}
	if pruneStateBytesLocal != 7_000 || pruneStateBytesRemote != 24_000 || pruneGoalTurns != 3 || compactThresholdPct != 70 {
		t.Error("a prune constant changed: update docs/jev-jobs.md and this test")
	}
	if fmt.Sprint(pruneInputCaps) != "[1000 200 60]" {
		t.Errorf("input caps changed to %v: update docs/jev-jobs.md", pruneInputCaps)
	}
}

// TestJevJobsPageListsTheAlwaysKeptTools keeps the pinned tools in the docs.
func TestJevJobsPageListsTheAlwaysKeptTools(t *testing.T) {
	doc := docparity.Read(t, "docs/jev-jobs.md")
	for name := range alwaysKeepTools {
		if !strings.Contains(doc, "`"+name+"`") {
			t.Errorf("docs/jev-jobs.md does not list the always-kept tool %q", name)
		}
	}
}
