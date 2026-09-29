package jev

import (
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestJevJobsPageStatesTheThresholds keeps the documented thresholds equal to
// the constants the jobs read.
func TestJevJobsPageStatesTheThresholds(t *testing.T) {
	doc := docparity.Read(t, "docs/jev-jobs.md")
	for name, v := range map[string]float64{"DropAt": DropAt, "KeepAt": KeepAt, "StrongAt": StrongAt, "SwapAt": SwapAt} {
		row := fmt.Sprintf("| `%s` | %.2f |", name, v)
		if !strings.Contains(doc, row) {
			t.Errorf("docs/jev-jobs.md lacks the row %q", row)
		}
	}
}

// TestJevJobsPageStatesTheBatchLimits keeps the documented limits equal to the
// constants.
func TestJevJobsPageStatesTheBatchLimits(t *testing.T) {
	doc := docparity.Read(t, "docs/jev-jobs.md")
	for _, want := range []string{
		fmt.Sprintf("up to %d items", maxItemsRemote),
		fmt.Sprintf("%d requests in flight", scoreWorkers),
		fmt.Sprintf("at most %d items", maxItemsLocal),
		fmt.Sprintf("at most %d requests", defaultRequests),
		fmt.Sprintf("%d of them", 4096),
		fmt.Sprintf("2 to %d options", MaxPickOptions),
	} {
		if !strings.Contains(doc, strings.TrimSuffix(want, " of them")) {
			t.Errorf("docs/jev-jobs.md does not state %q", want)
		}
	}
}
