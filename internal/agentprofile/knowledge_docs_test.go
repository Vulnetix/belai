package agentprofile

import (
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestProfilesPageStatesTheKnowledgeAndSyncLimits keeps the numbers and the
// protected paths docs/agent-profiles.md gives equal to the ones the validator
// enforces.
func TestProfilesPageStatesTheKnowledgeAndSyncLimits(t *testing.T) {
	doc := docparity.Read(t, "docs/agent-profiles.md")
	for _, want := range []string{
		fmt.Sprintf("up to %d files or directories", MaxKnowledgePaths),
		fmt.Sprintf("Up to %d entries", MaxSyncPaths),
		fmt.Sprintf("one to %d distinct paths", MaxKnowledgePaths),
		fmt.Sprintf("at most %d distinct", MaxSyncPaths),
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/agent-profiles.md does not state %q", want)
		}
	}
	// Every path the validator treats as protected is named on the page.
	for _, want := range []string{".git", ".vulnetix/belai", ".vulnetix/settings.json", ".vulnetix/credentials.json", "memory.yaml", "vex/", "quality/"} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/agent-profiles.md does not name the protected path %q", want)
		}
	}
	// The page describes the protection the code applies.
	for _, rel := range []string{".git/config", ".vulnetix/belai/x", ".vulnetix/settings.json", ".vulnetix/credentials.json"} {
		if !ProtectedRead(rel) {
			t.Errorf("%s must be protected from reads", rel)
		}
	}
	for _, rel := range []string{".vulnetix/memory.yaml", ".vulnetix/vex/a.json", ".vulnetix/quality/q.json", ".vulnetix/sast.sarif"} {
		if !ProtectedWrite(rel) || ProtectedRead(rel) {
			t.Errorf("%s is read only: protected from writes, not from reads", rel)
		}
	}
	for _, rel := range []string{"docs/NOTES.md", ".vulnetix/crews/delivery.md", "reference/standards/a.md"} {
		if ProtectedRead(rel) || ProtectedWrite(rel) {
			t.Errorf("%s is an ordinary path the profile may define", rel)
		}
	}
}
