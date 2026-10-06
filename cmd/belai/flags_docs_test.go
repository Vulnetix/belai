package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

// commandLine matches a `belai ...` invocation written in the docs, up to the
// end of the line, a closing backtick, or the next shell operator, so a flag of
// the tool it is piped into is never read as one of Belai's.
var commandLine = regexp.MustCompile("(?:^|[\\s`$>(])belai((?: +[^\\s`|&;<>()]+)*)")

// TestEveryFlagInADocumentedCommandExists is the reverse of
// TestEveryFlagIsDocumented: a flag the docs tell you to type after `belai`
// must be one the command line defines, so a renamed or removed flag cannot
// linger in an example.
func TestEveryFlagInADocumentedCommandExists(t *testing.T) {
	real := map[string]bool{"h": true, "help": true, "version": true}
	for _, n := range docparity.FlagNames(t, ".") {
		real[n] = true
	}
	checked := 0
	flag := regexp.MustCompile(`^--?([A-Za-z][A-Za-z0-9-]*)(=.*)?$`)
	scan := func(label, text string) {
		for i, line := range strings.Split(text, "\n") {
			for _, m := range commandLine.FindAllStringSubmatch(line, -1) {
				for _, tok := range strings.Fields(m[1]) {
					tok = strings.TrimRight(tok, ".,:;)")
					g := flag.FindStringSubmatch(tok)
					if g == nil {
						continue
					}
					checked++
					if !real[g[1]] {
						t.Errorf("%s:%d tells you to run `belai ... %s`, but belai defines no flag -%s", label, i+1, tok, g[1])
					}
				}
			}
		}
	}
	root := docparity.Root(t)
	entries, err := os.ReadDir(filepath.Join(root, "docs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		scan("docs/"+e.Name(), docparity.Read(t, "docs/"+e.Name()))
	}
	scan("README.md", docparity.Read(t, "README.md"))
	if checked < 20 {
		t.Fatalf("only %d flags were checked; the command-line pattern is wrong", checked)
	}
}
