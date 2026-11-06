package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/repomap"
)

func newInitApp(t *testing.T) (*App, string) {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	dir := t.TempDir()
	a := New(Options{Workdir: dir})
	a.repoMap = repomap.Map{Module: dir, Branch: "main", Layout: []repomap.DirSummary{{Name: "cmd\x1b[31m", Files: 2}}}
	return a, dir
}

func lastInitMsg(a *App) string { return a.messages[len(a.messages)-1].Text() }

func TestInitRefusesWhenAnInstructionFileExists(t *testing.T) {
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		a, dir := newInitApp(t)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("mine\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		a.mode = "agent"
		if cmd := a.initCommand(); cmd != nil {
			t.Errorf("%s: /init started a turn", name)
		}
		if !strings.Contains(lastInitMsg(a), name+" already exists") || strings.TrimSpace(a.editor.Value()) != "" {
			t.Errorf("%s: message %q, composer %q", name, lastInitMsg(a), a.editor.Value())
		}
		b, _ := os.ReadFile(filepath.Join(dir, name))
		if string(b) != "mine\n" {
			t.Errorf("%s was modified", name)
		}
	}
}

func TestInitRefusesADanglingLink(t *testing.T) {
	a, dir := newInitApp(t)
	if err := os.Symlink(filepath.Join(dir, "nowhere"), filepath.Join(dir, "AGENTS.md")); err != nil {
		t.Skip("symlinks unavailable")
	}
	a.mode = "agent"
	if cmd := a.initCommand(); cmd != nil || !strings.Contains(lastInitMsg(a), "already exists") {
		t.Errorf("a link at AGENTS.md was treated as absent: %q", lastInitMsg(a))
	}
}

func TestInitRefusedInPlanMode(t *testing.T) {
	a, _ := newInitApp(t)
	a.mode = "plan"
	if cmd := a.initCommand(); cmd != nil || !strings.Contains(lastInitMsg(a), "plan mode") {
		t.Errorf("plan mode: %q", lastInitMsg(a))
	}
	if a.mode != "plan" {
		t.Error("/init left plan mode")
	}
}

func TestInitComposesFixedRequestWithSanitisedFacts(t *testing.T) {
	a, _ := newInitApp(t)
	a.mode = "agent"
	a.initCommand()
	got := a.editor.Value()
	for _, want := range []string{"Draft an AGENTS.md", "Write tool", "Repository map (harness-computed facts", "root: "} {
		if !strings.Contains(got, want) {
			t.Errorf("the request lacks %q:\n%s", want, got)
		}
	}
	if strings.ContainsRune(got, 0x1b) {
		t.Error("an escape sequence from a directory name reached the request")
	}
}

func TestInitIsADocumentedCommand(t *testing.T) {
	if _, ok := NewRegistry(t.TempDir()).Command("init"); !ok {
		t.Fatal("/init is not registered")
	}
}
