package main

import (
	"regexp"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestEveryFlagIsDocumented keeps the documentation in step with the command
// line: every flag the main command and its subcommands define appears as
// -name (or --name) somewhere under docs/ or in the README.
func TestEveryFlagIsDocumented(t *testing.T) {
	docs := docparity.ReadDir(t, "docs") + docparity.Read(t, "README.md")
	names := docparity.FlagNames(t, ".")
	if len(names) < 60 {
		t.Fatalf("only %d flags found; the walk is wrong: %v", len(names), names)
	}
	for _, name := range names {
		re := regexp.MustCompile(`(^|[^A-Za-z0-9])--?` + regexp.QuoteMeta(name) + `([^A-Za-z0-9-]|$)`)
		if !re.MatchString(docs) {
			t.Errorf("the flag -%s is not documented under docs/ or in the README", name)
		}
	}
}
