package repomap

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func fixtureRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.test/repo\n\ngo 1.22\n")
	write("main.go", "package main\n\nfunc main() {}\n")
	write("cmd/tool/main.go", "package main\n\nfunc main() {}\n")
	write("pkg/x.go", "package pkg\n")
	write("justfile", "build:\n\tgo build ./...\n")
	write("AGENTS.md", "some instructions\n")
	write("README.md", "prose\n")

	// git init and a commit so gitinfo.Detect resolves.
	if _, err := exec.LookPath("git"); err != nil {
		return root
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		_ = cmd.Run()
	}
	run("init", "-q")
	run("add", ".")
	run("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "init")
	return root
}

func TestScanFixtureRepo(t *testing.T) {
	root := fixtureRepo(t)
	m := Scan(context.Background(), root)
	if m.Module != root {
		t.Fatalf("Module = %q, want %q", m.Module, root)
	}
	if m.Head == "" {
		t.Fatal("Head should be set after a commit")
	}
	if len(m.Commands.Build) == 0 || len(m.Commands.Test) == 0 {
		t.Fatalf("Commands = %+v, want go/just build and test", m.Commands)
	}
	if len(m.Entrypoints) < 2 {
		t.Fatalf("Entrypoints = %v, want main.go and cmd/tool/main.go", m.Entrypoints)
	}
	foundGo := false
	for _, l := range m.Languages {
		if l.Ext == "go" && l.Files >= 3 {
			foundGo = true
		}
	}
	if !foundGo {
		t.Fatalf("Languages = %v, want go with files", m.Languages)
	}
	foundAgents := false
	for _, a := range m.AgentsFiles {
		if a.Name == "AGENTS.md" && a.Size > 0 {
			foundAgents = true
		}
	}
	if !foundAgents {
		t.Fatalf("AgentsFiles = %v, want AGENTS.md", m.AgentsFiles)
	}
}

func TestScanNonRepoReturnsEmpty(t *testing.T) {
	m := Scan(context.Background(), t.TempDir())
	if m.Module != "" || len(m.Languages) != 0 {
		t.Fatalf("non-repo scan should be empty, got %+v", m)
	}
}

func TestCommandsTableNeverInfersFromProse(t *testing.T) {
	root := t.TempDir()
	// A README that *mentions* commands must not become detected commands.
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("run: go test ./...\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := commands(root, justRecipes(root))
	if len(c.Build) != 0 || len(c.Test) != 0 {
		t.Fatalf("commands inferred from prose: %+v", c)
	}
}

func TestJustRecipesHeadersOnly(t *testing.T) {
	root := t.TempDir()
	just := "set shell := [\"bash\", \"-uc\"]\nbinary := \"signet\"\n# a comment: not a recipe\ndefault:\n    @just --list\nbuild:\n    go build ./...\ntest *ARGS:\n    go test ./... {{ARGS}}\n@check: fmt-check test\n[private]\nfmt-check:\n    gofmt -l .\n"
	if err := os.WriteFile(filepath.Join(root, "justfile"), []byte(just), 0o600); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(justRecipes(root), " ")
	if want := "default build test check fmt-check"; got != want {
		t.Fatalf("recipes = %q, want %q", got, want)
	}
	c := commands(root, justRecipes(root))
	if strings.Join(c.Test, ";") != "just check;just test" || strings.Join(c.Build, ";") != "just build" {
		t.Fatalf("commands = %+v", c)
	}
	if len(c.Lint) != 0 || len(c.Fmt) != 0 {
		t.Fatalf("a justfile without lint/fmt recipes must not advertise them: %+v", c)
	}
}

func TestParseStatusCapsAndCounts(t *testing.T) {
	var b strings.Builder
	for i := 0; i < maxChanged+5; i++ {
		fmt.Fprintf(&b, " M file%d.go\n", i)
	}
	b.WriteString("?? new.txt\n")
	rows, total := parseStatus(b.String())
	if total != maxChanged+6 {
		t.Fatalf("total = %d", total)
	}
	if len(rows) != maxChanged {
		t.Fatalf("rows = %d, want cap %d", len(rows), maxChanged)
	}
	if rows[0].Status != "M" || rows[0].Path != "file0.go" {
		t.Fatalf("row0 = %+v", rows[0])
	}
}

func TestRedactRemote(t *testing.T) {
	cases := map[string]string{
		"https://user:ghp_secret@github.com/o/r.git": "https://github.com/o/r.git",
		"https://github.com/o/r.git":                 "https://github.com/o/r.git",
		"git@github.com:o/r.git":                     "git@github.com:o/r.git",
	}
	for in, want := range cases {
		if got := redactRemote(in); got != want {
			t.Errorf("redactRemote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseBranch(t *testing.T) {
	cases := map[string]string{
		"## main...origin/main [ahead 1]\n M a.go": "main",
		"## feature/x":               "feature/x",
		"## HEAD (no branch)\n?? b":  "",
		"## No commits yet on trunk": "trunk",
		" M a.go":                    "",
	}
	for in, want := range cases {
		if got := parseBranch(in); got != want {
			t.Fatalf("parseBranch(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseStatusSkipsTheBranchHeader(t *testing.T) {
	rows, total := parseStatus("## main...origin/main\n M a.go\n")
	if total != 1 || len(rows) != 1 || rows[0].Path != "a.go" {
		t.Fatalf("rows = %+v total = %d", rows, total)
	}
}
