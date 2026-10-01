package rolemanager

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestTestingPageStatesTheReportCap pins the 600 character cap on the fast
// model's test report.
func TestTestingPageStatesTheReportCap(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/testing.md")), " ")
	if !strings.Contains(doc, "capped at 600 characters") || maxTestReportRunes != 600 {
		t.Errorf("the page says 600 characters; the cap is %d", maxTestReportRunes)
	}
}
