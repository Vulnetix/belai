package kiromodels

import (
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestKiroPageStatesTheCatalogueLimits pins the catalogue paging and cache
// lifetime docs/kiro.md states.
func TestKiroPageStatesTheCatalogueLimits(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/kiro.md")), " ")
	for _, want := range []string{
		"It follows up to five pages and caches the result for five minutes",
		"remembered as empty for five minutes",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/kiro.md does not say %q", want)
		}
	}
	if maxPages != 5 || TTL != 5*time.Minute {
		t.Errorf("maxPages %d and TTL %v disagree with the page", maxPages, TTL)
	}
}
