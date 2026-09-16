package tui

import "testing"

// TestPermissionKnownToolNamesIncludesMutatingTools pins that the permission
// editor's known-tool set derives from Default().Names() and therefore picks
// Write and Edit up automatically — the editor must offer them, not silently
// omit them.
func TestPermissionKnownToolNamesIncludesMutatingTools(t *testing.T) {
	names := knownToolNames(t.TempDir())
	for _, want := range []string{"write", "edit", "read", "bash"} {
		if !names[want] {
			t.Fatalf("knownToolNames missing %q: %v", want, names)
		}
	}
}
