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

// notEnvWired are variables a page names only to say they are not read.
var notEnvWired = map[string]bool{
	"BELAI_CLASSIFIER_CHUNK_BYTES":       true,
	"BELAI_CLASSIFIER_CHUNK_CONCURRENCY": true,
}

// TestEveryBelaiOrVulnetixVariableTheDocsNameExists is the reverse check: a
// BELAI_ or VULNETIX_ variable a page names must appear somewhere in the code,
// scripts or workflows, so a page cannot keep describing a variable that was
// removed. The two the development page says are not yet wired are the only
// exemptions.
func TestEveryBelaiOrVulnetixVariableTheDocsNameExists(t *testing.T) {
	root := docparity.Root(t)
	docs := docparity.ReadDir(t, "docs") + docparity.Read(t, "README.md") + docparity.Read(t, "AGENTS.md")
	names := map[string]bool{}
	for _, m := range regexp.MustCompile(`\b(?:BELAI|VULNETIX)_[A-Z0-9_]+`).FindAllString(docs, -1) {
		names[m] = true
	}
	if len(names) < 30 {
		t.Fatalf("only %d variable names found in the docs; the pattern is wrong", len(names))
	}
	var src strings.Builder
	for _, dir := range []string{"cmd", "internal", "tools", "bench", "e2e", ".github", filepath.Join("site", "scripts")} {
		_ = filepath.WalkDir(filepath.Join(root, dir), func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || filepath.Base(p) == "env_test.go" {
				return nil // this file names the exemptions, so it must not count as a use
			}
			switch filepath.Ext(p) {
			case ".go", ".yml", ".yaml", ".py", ".sh", ".mjs":
				if b, rerr := os.ReadFile(p); rerr == nil {
					src.Write(b)
				}
			}
			return nil
		})
	}
	for _, f := range []string{"justfile", "install.sh"} {
		if b, err := os.ReadFile(filepath.Join(root, f)); err == nil {
			src.Write(b)
		}
	}
	var sorted []string
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	for _, n := range sorted {
		if notEnvWired[n] {
			continue
		}
		if !strings.Contains(src.String(), n) {
			t.Errorf("the docs name %s, but nothing in the repository uses it", n)
		}
	}
	for n := range notEnvWired {
		if !strings.Contains(docs, "`"+n+"`") || strings.Contains(src.String(), n) {
			t.Errorf("%s is exempt as documented-but-unwired; that no longer holds", n)
		}
	}
}
