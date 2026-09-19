package repomap

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
	c := commands(root)
	if len(c.Build) != 0 || len(c.Test) != 0 {
		t.Fatalf("commands inferred from prose: %+v", c)
	}
}
