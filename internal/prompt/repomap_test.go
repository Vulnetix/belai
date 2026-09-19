package prompt

import (
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/repomap"
)

func TestRepoMapBlockRendersFacts(t *testing.T) {
	m := repomap.Map{
		Module:      "/repo",
		Branch:      "main",
		Head:        "abc1234",
		Dirty:       true,
		Languages:   []repomap.LangCount{{Ext: "go", Files: 5}},
		Commands:    repomap.Commands{Build: []string{"go build ./..."}, Test: []string{"go test ./..."}},
		Entrypoints: []string{"main.go"},
		Layout:      []repomap.DirSummary{{Name: "internal", Files: 20}},
		AgentsFiles: []repomap.AgentsFile{{Name: "AGENTS.md", Size: 10}},
	}
	block := RepoMapBlock(m)
	for _, want := range []string{"/repo", "main abc1234", "dirty", "go(5)", "go build ./...", "go test ./...", "main.go", "internal(20)", "AGENTS.md(10B)"} {
		if !strings.Contains(block, want) {
			t.Fatalf("block missing %q:\n%s", want, block)
		}
	}
}

func TestRepoMapBlockEmptyMapRendersNothing(t *testing.T) {
	if RepoMapBlock(repomap.Map{}) != "" {
		t.Fatal("empty map must render nothing")
	}
}

func TestRepoMapBlockNeverCarriesProse(t *testing.T) {
	// The Map has no prose field by construction; this test pins that the
	// rendered block contains only the fixed fact lines, never a README body.
	m := repomap.Map{Module: "/repo", Commands: repomap.Commands{Build: []string{"go build ./..."}}}
	if strings.Contains(RepoMapBlock(m), "some repository prose") {
		t.Fatal("repo map block must not carry repository prose")
	}
}

// TestAssembledSystemPromptCarriesOnlyMapFacts pins the security spot-check:
// the assembled system prompt renders the repo-map facts into the system text
// but must never carry a repository file's prose alongside them.
func TestAssembledSystemPromptCarriesOnlyMapFacts(t *testing.T) {
	m := repomap.Map{
		Module:      "/repo",
		Branch:      "main",
		Head:        "abc1234",
		Commands:    repomap.Commands{Build: []string{"go build ./..."}, Test: []string{"go test ./..."}},
		AgentsFiles: []repomap.AgentsFile{{Name: "AGENTS.md", Size: 42}},
	}
	sys, err := System(Options{RepoMap: RepoMapBlock(m), Provider: "openai", Model: "m"})
	if err != nil {
		t.Fatalf("System: %v", err)
	}
	for _, want := range []string{"Repository map", "/repo", "abc1234", "go build ./...", "go test ./...", "AGENTS.md(42B)"} {
		if !strings.Contains(sys, want) {
			t.Fatalf("system prompt missing map fact %q:\n%s", want, sys)
		}
	}
	for _, prose := range []string{"# Project Readme", "DO NOT USE IN PRODUCTION", "some repository prose"} {
		if strings.Contains(sys, prose) {
			t.Fatalf("system prompt carried repository prose %q:\n%s", prose, sys)
		}
	}
}
