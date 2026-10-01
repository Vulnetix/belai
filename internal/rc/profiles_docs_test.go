package rc

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestRemoteControlPageSaysKnowledgeAndSyncTravelWithAProfile keeps the install
// rules on the page equal to the ones the daemon applies: the two blocks are
// carried by a backup, kept by an install and, on a replace, this host's are
// kept only when the library copy lists none.
func TestRemoteControlPageSaysKnowledgeAndSyncTravelWithAProfile(t *testing.T) {
	doc := docparity.Read(t, "docs/remote-control.md")
	for _, want := range []string{
		"`knowledge` and `workspace.sync` blocks are part of it",
		"a backup\n  carries them",
		"an install keeps them",
		"keeps the entries already on this host when that copy lists none",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/remote-control.md does not say %q", want)
		}
	}
	if strings.Contains(doc, "cannot list local documents") || strings.Contains(doc, "install is refused") {
		t.Error("the page still describes the old refusal of a profile that lists documents or syncs files")
	}
}
