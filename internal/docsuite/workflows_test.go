package docsuite

import (
	"regexp"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// jobNames lists the jobs of a workflow file: the two-space indented keys under
// jobs:.
func jobNames(t *testing.T, workflow string) []string {
	t.Helper()
	src := docparity.Read(t, workflow)
	i := strings.Index(src, "\njobs:\n")
	if i < 0 {
		t.Fatalf("%s has no jobs", workflow)
	}
	var out []string
	for _, m := range regexp.MustCompile(`(?m)^  ([a-z][a-z-]*):\s*$`).FindAllStringSubmatch(src[i:], -1) {
		out = append(out, m[1])
	}
	return out
}

// TestDevelopmentPageDescribesTheWorkflows keeps the CI and release paragraph of
// docs/development.md equal to the workflow files: every CI job is named, the
// release triggers on a v* tag, and the Pages workflow's paths are stated.
func TestDevelopmentPageDescribesTheWorkflows(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/development.md")), " ")
	ci := jobNames(t, ".github/workflows/ci.yml")
	if len(ci) != 3 {
		t.Fatalf("ci.yml has %d jobs %v; update the page and this test together", len(ci), ci)
	}
	for _, j := range ci {
		if !strings.Contains(doc, "`"+j+"`") {
			t.Errorf("docs/development.md does not name the CI job `%s`", j)
		}
	}
	if !strings.Contains(doc, "as three jobs") {
		t.Error("docs/development.md does not say CI has three jobs")
	}
	ciSrc := docparity.Read(t, ".github/workflows/ci.yml")
	for _, want := range []string{"go vet ./...", "go test -race ./...", "go test -tags belai_bert ./internal/mlclassify/...", "go test -tags belai_bert_jailbreak ./internal/mlclassify/...", "go test -tags belai_voice ./internal/voice/..."} {
		if !strings.Contains(ciSrc, want) {
			t.Errorf("ci.yml no longer runs %q, which the page describes", want)
		}
	}
	if !strings.Contains(ciSrc, "tags: ['**']") || !strings.Contains(ciSrc, "dependabot/**") {
		t.Error("ci.yml no longer runs on tags or skips Dependabot branches, as the page says")
	}
	release := docparity.Read(t, ".github/workflows/release.yml")
	if !strings.Contains(release, "- 'v*'") || !strings.Contains(doc, "fires on a `v*` tag") {
		t.Error("the release trigger on a v* tag is not as the page says")
	}
	for _, variant := range []string{"belai-bert-guardrails", "belai-bert-guardrails-jailbreak", "belai-no-classifier"} {
		if !strings.Contains(doc, "`"+variant+"`") {
			t.Errorf("docs/development.md does not name the release family %s", variant)
		}
	}
	pages := docparity.Read(t, ".github/workflows/pages.yml")
	if !strings.Contains(pages, `"site/**"`) || !strings.Contains(doc, "pushes to `main` that touch `site/**` or the workflow itself") {
		t.Error("the Pages workflow's paths are not as the page says")
	}
}

// TestDevelopmentPageRecipesExist keeps every `just` recipe the page names in
// the justfile.
func TestDevelopmentPageRecipesExist(t *testing.T) {
	doc := docparity.Read(t, "docs/development.md")
	just := docparity.Read(t, "justfile")
	seen := map[string]bool{}
	for _, m := range regexp.MustCompile("`just ([a-z][a-z0-9-]*)").FindAllStringSubmatch(doc, -1) {
		r := m[1]
		if seen[r] {
			continue
		}
		seen[r] = true
		if !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(r) + `( [^:]*)?:`).MatchString(just) {
			t.Errorf("docs/development.md names `just %s`, but the justfile has no such recipe", r)
		}
	}
	if len(seen) < 20 {
		t.Fatalf("only %d recipes found on the page; the pattern is wrong", len(seen))
	}
}
