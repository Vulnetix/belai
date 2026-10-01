package docsuite

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// envRead matches an environment lookup with a literal name: os.Getenv,
// os.LookupEnv, and the injected Getenv fields the packages use for tests.
var envRead = regexp.MustCompile(`(?:Getenv|LookupEnv)\("([A-Za-z_][A-Za-z0-9_]*)"\)`)

// TestEveryEnvironmentVariableTheCodeReadsIsDocumented keeps the docs complete:
// a variable read with a literal name in non-test code under cmd/ or internal/
// appears somewhere under docs/, in the README or in AGENTS.md. A new variable
// without a mention fails here.
func TestEveryEnvironmentVariableTheCodeReadsIsDocumented(t *testing.T) {
	root := docparity.Root(t)
	docs := docparity.ReadDir(t, "docs") + docparity.Read(t, "README.md") + docparity.Read(t, "AGENTS.md")
	names := map[string][]string{}
	for _, dir := range []string{"cmd", "internal"} {
		_ = filepath.WalkDir(filepath.Join(root, dir), func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			b, rerr := os.ReadFile(p)
			if rerr != nil {
				return nil
			}
			for _, m := range envRead.FindAllStringSubmatch(string(b), -1) {
				rp, _ := filepath.Rel(root, p)
				names[m[1]] = append(names[m[1]], rp)
			}
			return nil
		})
	}
	if len(names) < 25 {
		t.Fatalf("only %d environment variables found; the pattern is wrong", len(names))
	}
	var sorted []string
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	for _, n := range sorted {
		if !strings.Contains(docs, n) {
			t.Errorf("the code reads %s (%s) but no page mentions it", n, names[n][0])
		}
	}
}
