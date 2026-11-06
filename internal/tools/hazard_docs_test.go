package tools

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
	"github.com/vulnetix/belai/internal/shellsafe"
)

// TestHazardPageListsEveryId keeps docs/hazard-rails.md equal to the closed
// sets in code: each shell id and each file class has its row, each fixed
// protected name is on the page, and no id-shaped token on the page is missing
// from the code.
func TestHazardPageListsEveryId(t *testing.T) {
	doc := docparity.Read(t, "docs/hazard-rails.md")
	known := map[string]bool{}
	for _, id := range shellsafe.HazardIDs {
		known[id] = true
		if !strings.Contains(doc, "| `"+id+"` |") {
			t.Errorf("docs/hazard-rails.md has no table row for %s", id)
		}
	}
	for _, id := range HazardPathClasses {
		known[id] = true
		if !strings.Contains(doc, "| `"+id+"` |") {
			t.Errorf("docs/hazard-rails.md has no table row for %s", id)
		}
	}
	for _, r := range ProtectedRules {
		if r.Kind == "credential" {
			continue
		}
		if !strings.Contains(doc, r.Match) {
			t.Errorf("docs/hazard-rails.md does not name the protected %s %q", r.Kind, r.Match)
		}
	}
	for _, key := range []string{"permissions.hazard_ask", "permissions.hazard_yolo_waive", "permissions.protected_paths", "/hazards", "hazard_ask", "hazard.asked", "hazard.denied"} {
		if !strings.Contains(doc, key) {
			t.Errorf("docs/hazard-rails.md does not mention %s", key)
		}
	}
	// Every backticked id on the page that looks like an identifier of this
	// feature is one the code returns.
	for _, prefix := range []string{"exec.", "path.", "hazard."} {
		for _, f := range strings.Split(doc, "`") {
			if strings.HasPrefix(f, prefix) && !strings.ContainsAny(f, " |") && !known[f] && f != "hazard.asked" && f != "hazard.denied" && f != "hazard.opaque" {
				t.Errorf("docs/hazard-rails.md names %q, which is not a hazard id", f)
			}
		}
	}
}
