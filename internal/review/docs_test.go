package review

import (
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestReviewPageNamesEveryExportedFunction keeps docs/review.md in step with
// the package.
func TestReviewPageNamesEveryExportedFunction(t *testing.T) {
	docparity.RequireMentions(t, "docs/review.md", docparity.ExportedFuncs(t, "."))
}

// TestReviewPageStatesTheClosedSetsAndLimits pins the page to the code.
func TestReviewPageStatesTheClosedSetsAndLimits(t *testing.T) {
	doc := docparity.Read(t, "docs/review.md")
	for _, c := range Criteria {
		if !strings.Contains(doc, "| `"+c.ID+"` |") {
			t.Errorf("docs/review.md has no row for the criterion %q", c.ID)
		}
	}
	for _, s := range Severities {
		if !strings.Contains(doc, "`"+string(s)+"`") {
			t.Errorf("docs/review.md does not list the severity %q", s)
		}
	}
	for _, v := range Verdicts {
		if !strings.Contains(doc, "`"+string(v)+"`") {
			t.Errorf("docs/review.md does not list the verdict %q", v)
		}
	}
	for _, want := range []string{
		fmt.Sprintf("at most %d finding lines", MaxFindings),
		fmt.Sprintf("%d bytes", MaxBodyBytes),
		fmt.Sprintf("%d files", MaxDiffFiles),
		fmt.Sprintf("newest %d reviews", Keep),
		"`" + PostToolName + "`", "`" + ProjectRubricPath + "`", "`" + ProfileName + "`",
		"reviews/<repository>-<head>.json",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/review.md does not state %q", want)
		}
	}
	for _, tool := range Tools {
		if !strings.Contains(doc, "`"+tool+"`") {
			t.Errorf("docs/review.md does not list the tool %q", tool)
		}
	}
}
