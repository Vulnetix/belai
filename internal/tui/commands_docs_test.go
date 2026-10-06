package tui

import (
	"regexp"
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

// notCommands are the `/word` tokens in the docs that are not slash commands:
// file system paths, URL path segments, provider endpoints and the prefixes of
// the `/prompt:NAME`, `/process:NAME` and `/agent:NAME` forms.
var notCommands = map[string]bool{
	// paths and mount points
	"bin": true, "sbin": true, "sys": true, "tmp": true, "dev": true, "dir": true, "run": true, "proc": true,
	// URL paths and provider endpoints
	"v1": true, "compat": true, "responses": true, "models": true, "health": true, "openai": true,
	"completion": true, "command": true, "deploy": true, "repository": true,
	// the first letters of a command being completed, in examples
	"a": true, "c": true, "p": true, "pmt": true, "dpl": true,
	// the prefixes of /prompt:NAME and /process:NAME
	"prompt": true,
}

// TestDocsNameOnlyRealSlashCommands is the reverse check: a `/word` the docs put
// in code formatting is a registered command or alias, or one of the known
// non-commands above. A page cannot keep describing a command that was never
// there or was removed (as `/fork` was, when it is `/tree fork`).
func TestDocsNameOnlyRealSlashCommands(t *testing.T) {
	docs := docparity.ReadDir(t, "docs") + docparity.Read(t, "README.md")
	r := NewRegistry(t.TempDir())
	checked := 0
	seen := map[string]bool{}
	for _, m := range regexp.MustCompile("`/([a-z][a-z0-9-]*)[ `:]").FindAllStringSubmatch(docs, -1) {
		name := m[1]
		if seen[name] {
			continue
		}
		seen[name] = true
		if notCommands[name] {
			continue
		}
		checked++
		if _, ok := r.Command(name); !ok {
			t.Errorf("the docs name the slash command `/%s`, which the TUI does not register", name)
		}
	}
	if checked < 30 {
		t.Fatalf("only %d slash commands checked; the pattern is wrong", checked)
	}
	// /fork was never a command: forking is /tree fork <id>.
	if _, ok := r.Command("fork"); ok {
		t.Error("/fork is registered; the page should then document it, not /tree fork")
	}
	if !strings.Contains(docparity.Read(t, "docs/acp.md"), "`/tree fork <id>` is TUI only") {
		t.Error("docs/acp.md does not name /tree fork as the TUI-only fork command")
	}
}
