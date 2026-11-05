package fuzzy

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestEditPageNamesTheLineMatcher keeps docs/edit.md in step with the code:
// every exported function of the line matcher is named on the page. Rank,
// Score and Strings belong to the ranking half of the package.
func TestEditPageNamesTheLineMatcher(t *testing.T) {
	ranking := map[string]bool{"Rank": true, "Score": true, "Strings": true}
	var names []string
	for _, n := range docparity.ExportedFuncs(t, ".") {
		if !ranking[n] {
			names = append(names, n)
		}
	}
	if len(names) < 6 {
		t.Fatalf("only %d line matcher functions found: %v", len(names), names)
	}
	docparity.RequireMentions(t, "docs/edit.md", names)
	doc := docparity.Read(t, "docs/edit.md")
	for _, tier := range []Tier{TierLineEnding, TierTrailing, TierIndent} {
		if !strings.Contains(doc, "`"+tier.String()+"`") {
			t.Errorf("docs/edit.md does not name the tier %q", tier)
		}
	}
	for _, d := range []Differs{DiffersLineEnding, DiffersTrailing, DiffersIndent, DiffersContent} {
		if !strings.Contains(doc, "`"+string(d)+"`") {
			t.Errorf("docs/edit.md does not name the difference %q", d)
		}
	}
}
