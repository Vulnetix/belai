package prompt

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/repomap"
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
	for _, want := range []string{"/repo", "go(5)", "go build ./...", "go test ./...", "main.go", "internal(20)", "AGENTS.md(10B)"} {
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

func TestWorkspaceBlockRendersEachRoot(t *testing.T) {
	m1 := repomap.Map{Root: "/other", Module: "/other", Languages: []repomap.LangCount{{Ext: "go", Files: 1}}}
	m2 := repomap.Map{Root: "/third", Module: "/third"}
	block := WorkspaceBlock([]repomap.Map{m1, m2})
	for _, want := range []string{"Additional workspace directory maps", "Workspace directory 1 (/other)", "Workspace directory 2 (/third)"} {
		if !strings.Contains(block, want) {
			t.Fatalf("block missing %q:\n%s", want, block)
		}
	}
}

func TestWorkspaceBlockSkipsEmptyMaps(t *testing.T) {
	block := WorkspaceBlock([]repomap.Map{{Root: "/empty"}})
	if block != "" {
		t.Fatalf("expected empty block, got %q", block)
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
	for _, want := range []string{"Repository map", "/repo", "go build ./...", "go test ./...", "AGENTS.md(42B)"} {
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

// The system block must be byte-stable across turns, so nothing that moves
// with a commit or an edit may render into it.
func TestRepoMapBlockCarriesNoVolatileGitFacts(t *testing.T) {
	m := repomap.Map{
		Module:       "/repo",
		Branch:       "main",
		Head:         "abc1234",
		Dirty:        true,
		Changed:      []repomap.ChangedPath{{Status: "M", Path: "a.go"}},
		ChangedTotal: 1,
		Commands:     repomap.Commands{Build: []string{"go build ./..."}},
	}
	block := RepoMapBlock(m)
	for _, volatile := range []string{"abc1234", "dirty", "a.go", "changed"} {
		if strings.Contains(block, volatile) {
			t.Fatalf("system repo map carried volatile fact %q:\n%s", volatile, block)
		}
	}
	status := RepoStatusBlock(m)
	for _, want := range []string{"main abc1234", "(dirty)", "changed (1): M a.go", "harness-computed facts"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status block missing %q:\n%s", want, status)
		}
	}
}

func TestRepoStatusBlockEmptyWithoutGitFacts(t *testing.T) {
	if RepoStatusBlock(repomap.Map{}) != "" || RepoStatusBlock(repomap.Map{Module: "/repo"}) != "" {
		t.Fatal("a map with no git facts renders no status block")
	}
}
