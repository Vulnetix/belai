package repoindex

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// The fleet page tells a profile author what a handoff's repo argument can
// name, which is what this index holds. Its limits are the code's.
func TestFleetPageStatesTheIndexLimits(t *testing.T) {
	doc := docparity.Read(t, "docs/fleet.md")
	want := fmt.Sprintf("at most %d checkouts from at most %d", maxEntries, maxDirs)
	if !strings.Contains(strings.Join(strings.Fields(doc), " "), want) {
		t.Errorf("docs/fleet.md should say %q", want)
	}
	var skipped []string
	for d := range skipDirs {
		skipped = append(skipped, d)
	}
	sort.Strings(skipped)
	for _, d := range skipped {
		if !strings.Contains(doc, "`"+d+"`") {
			t.Errorf("docs/fleet.md does not name the skipped directory %q", d)
		}
	}
}
