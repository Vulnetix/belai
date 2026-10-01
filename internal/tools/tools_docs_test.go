package tools

import (
	"regexp"
	"sort"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// allToolNames is every tool name the harness can offer a model from this
// package: the default registry, each native in the local and cloud
// catalogues, and the repository tools.
func allToolNames(t *testing.T) []string {
	t.Helper()
	seen := map[string]bool{}
	for _, n := range Default(t.TempDir(), false).Names() {
		seen[n] = true
	}
	for _, c := range localCatalog() {
		seen[c.name] = true
	}
	for _, c := range cloudCatalog() {
		seen[c.name] = true
	}
	// Tools that are not catalogue entries name themselves; RepoFiles and
	// RepoRead are built from a repository index, and need only their names.
	seen[(&Vulnetix{}).Definition().Name] = true
	seen[(&RepoList{}).Definition().Name] = true
	seen["RepoFiles"], seen["RepoRead"] = true, true
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// TestEveryToolIsDocumented keeps the documentation in step with the tools: each
// name the model can be offered appears in code formatting (`Name` or
// `Name(…)`, the permission-rule form) under docs/ or in the README, so a
// common word such as Read or Sort in prose does not count.
func TestEveryToolIsDocumented(t *testing.T) {
	docs := docparity.ReadDir(t, "docs") + docparity.Read(t, "README.md")
	names := allToolNames(t)
	if len(names) < 40 {
		t.Fatalf("only %d tools found: %v", len(names), names)
	}
	for _, n := range names {
		if !regexp.MustCompile("`" + regexp.QuoteMeta(n) + "[`(]").MatchString(docs) {
			t.Errorf("the tool %s is not documented under docs/ or in the README", n)
		}
	}
}
