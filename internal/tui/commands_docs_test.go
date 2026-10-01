package tui

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestEverySlashCommandIsDocumented keeps the documentation in step with the
// registry: every command the TUI lists in its help and completion appears as
// "/name" somewhere under docs/ (or the README), so a command added without a
// word of documentation fails here.
func TestEverySlashCommandIsDocumented(t *testing.T) {
	docs := docparity.ReadDir(t, "docs") + docparity.Read(t, "README.md")
	r := NewRegistry(t.TempDir())
	for _, name := range r.Names() {
		if !strings.Contains(docs, "/"+name) {
			t.Errorf("the slash command /%s is not documented under docs/ or in the README", name)
		}
	}
}
