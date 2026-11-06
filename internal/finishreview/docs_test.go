package finishreview

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
	"github.com/vulnetix/belai/internal/workdiff"
)

// TestFinishReviewPageStatesTheChecks pins the kinds, the scratch table and the
// bounds docs/finish-review.md states to the code.
func TestFinishReviewPageStatesTheChecks(t *testing.T) {
	doc := docparity.Read(t, "docs/finish-review.md")
	flat := strings.Join(strings.Fields(doc), " ")
	for _, k := range Kinds {
		if !strings.Contains(doc, "| `"+string(k)+"` |") {
			t.Errorf("the page has no table row for kind %q", k)
		}
	}
	if !strings.Contains(flat, strings.Join(ScratchTable, " ")) {
		t.Errorf("the page does not list the scratch table %q", strings.Join(ScratchTable, " "))
	}
	for _, want := range []string{
		"at most 20 findings",
		"more changed files than the diff limit (300)",
		"`<state dir>/finish_review/<goal id>.json` (mode 0600 in a 0700 directory",
		"over 1 MiB",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("the page does not say %q", want)
		}
	}
	if MaxFindings != 20 || workdiff.MaxFiles != 300 || maxBaselineBytes != 1<<20 {
		t.Errorf("MaxFindings %d, workdiff.MaxFiles %d or maxBaselineBytes %d disagree with the page", MaxFindings, workdiff.MaxFiles, maxBaselineBytes)
	}
}

// TestPagePointsAtTheCode keeps the page's code pointers honest.
func TestPagePointsAtTheCode(t *testing.T) {
	doc := docparity.Read(t, "docs/finish-review.md")
	for _, want := range []string{"internal/finishreview", "internal/agent/finishreview.go", "internal/config/finishreview.go", "internal/workdiff"} {
		if !strings.Contains(doc, want) {
			t.Errorf("the page does not point at %s", want)
		}
	}
}
