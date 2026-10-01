package tools

import (
	"regexp"
	"sort"
	"strings"
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

// bulletNames returns the backticked names in the architecture page's bullet
// that starts with lead, up to the bullet's last line.
func bulletNames(t *testing.T, lead string) []string {
	t.Helper()
	doc := docparity.Read(t, "docs/architecture.md")
	i := regexp.MustCompile(`(?m)^- ` + regexp.QuoteMeta(lead)).FindStringIndex(doc)
	if i == nil {
		t.Fatalf("no bullet starting %q", lead)
	}
	rest := doc[i[0]:]
	end := regexp.MustCompile(`\n- |\n\n`).FindStringIndex(rest[2:])
	if end != nil {
		rest = rest[:end[0]+2]
	}
	var out []string
	for _, m := range regexp.MustCompile("`([A-Za-z0-9]+)`").FindAllStringSubmatch(rest, -1) {
		out = append(out, m[1])
	}
	return out
}

// TestNativeCatalogueBulletsMatchTheCode keeps the local and cloud lists in the
// architecture page's Native tool catalogue equal to the two code catalogues.
// Grep, Glob and Cd are named in the local bullet as the tools that stay apart.
func TestNativeCatalogueBulletsMatchTheCode(t *testing.T) {
	local := map[string]bool{}
	for _, c := range localCatalog() {
		local[c.name] = true
	}
	cloud := map[string]bool{}
	for _, c := range cloudCatalog() {
		cloud[c.name] = true
	}
	if len(local) < 20 || len(cloud) < 14 {
		t.Fatalf("catalogue sizes %d and %d look wrong", len(local), len(cloud))
	}
	apart := map[string]bool{"Grep": true, "Glob": true, "Cd": true}
	got := map[string]bool{}
	for _, n := range bulletNames(t, "Local utilities") {
		if apart[n] {
			continue
		}
		got[n] = true
		if !local[n] {
			t.Errorf("the page lists %s as a local utility, but the local catalogue has no such tool", n)
		}
	}
	for n := range local {
		if !got[n] {
			t.Errorf("the local catalogue has %s, which the page's local utilities bullet does not list", n)
		}
	}
	gotCloud := map[string]bool{}
	for _, n := range bulletNames(t, "Cloud/SaaS CLIs") {
		gotCloud[n] = true
		if !cloud[n] {
			t.Errorf("the page lists %s as a cloud CLI, but the cloud catalogue has no such tool", n)
		}
	}
	for n := range cloud {
		if !gotCloud[n] {
			t.Errorf("the cloud catalogue has %s, which the page's cloud bullet does not list", n)
		}
	}
	doc := docparity.Read(t, "docs/architecture.md")
	if !strings.Contains(strings.Join(strings.Fields(doc), " "), "Output is capped (64 KiB default)") {
		t.Error("the page does not state the 64 KiB native output cap")
	}
}
