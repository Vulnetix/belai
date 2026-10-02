package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/workdiff"
)

func diffTestRepo(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	g := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	g("init", "-q")
	for name, body := range map[string]string{"a.txt": "one\ntwo\n", "b.txt": "keep\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	g("add", ".")
	g("commit", "-q", "-m", "init")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\nTWO\nsecret-new-text\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "c.txt"), []byte("fresh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func newDiffApp(t *testing.T, dir string) *App {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	a := New(Options{Workdir: dir})
	a.width, a.height = 100, 40
	return a
}

// load runs the collection synchronously, as the program would deliver it.
func loadDiff(t *testing.T, a *App) {
	t.Helper()
	cmd := a.push(viewDiff)
	if cmd == nil {
		t.Fatal("entering the pane should start a load")
	}
	if !a.diffState.loading {
		t.Fatal("expected loading state")
	}
	if !strings.Contains(a.diffView(), "reading") {
		t.Fatal("loading view missing")
	}
	a.Update(cmd())
	if a.diffState.loading {
		t.Fatal("still loading")
	}
}

func TestDiffCommandRegistered(t *testing.T) {
	r := NewRegistry(t.TempDir())
	if _, ok := r.Command("diff"); !ok {
		t.Fatal("/diff not registered")
	}
}

func TestDiffPaneShowsChangesAndNavigates(t *testing.T) {
	dir := diffTestRepo(t)
	a := newDiffApp(t, dir)
	loadDiff(t, a)

	v := a.diffView()
	for _, want := range []string{"a.txt", "c.txt", "1 unstaged", "1 untracked", "secret-new-text"} {
		if !strings.Contains(v, want) {
			t.Fatalf("view missing %q:\n%s", want, v)
		}
	}
	_, _ = a.handleDiffKey(tea.KeyMsg{Type: tea.KeyRight})
	if a.diffState.selected != 1 {
		t.Fatalf("selected = %d", a.diffState.selected)
	}
	if v := a.diffView(); !strings.Contains(v, "fresh") {
		t.Fatalf("second file not shown:\n%s", v)
	}
	_, _ = a.handleDiffKey(tea.KeyMsg{Type: tea.KeyRight}) // wraps
	if a.diffState.selected != 0 {
		t.Fatalf("selected = %d after wrap", a.diffState.selected)
	}
	_, _ = a.handleDiffKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	_ = a.diffView() // clamps
	_, _ = a.handleDiffKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	if a.diffState.scroll != 0 {
		t.Fatalf("scroll = %d", a.diffState.scroll)
	}
	_, _ = a.handleDiffKey(tea.KeyMsg{Type: tea.KeyEsc})
	if a.view != viewChat {
		t.Fatalf("view = %v", a.view)
	}
}

func TestDiffPaneScrollsLongDiff(t *testing.T) {
	dir := diffTestRepo(t)
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString("line\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "long.txt"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newDiffApp(t, dir)
	a.height = 24
	loadDiff(t, a)
	for i, f := range a.diffState.snap.Files {
		if f.Path == "long.txt" {
			a.diffState.selected = i
		}
	}
	_ = a.diffView()
	_, _ = a.handleDiffKey(tea.KeyMsg{Type: tea.KeyPgDown})
	v := a.diffView()
	if a.diffState.scroll == 0 {
		t.Fatal("page down did not scroll")
	}
	if !strings.Contains(v, "/") || strings.Count(v, "\n") > a.height+2 {
		t.Fatalf("view overflows the screen (%d lines)", strings.Count(v, "\n"))
	}
}

func TestDiffPaneHidesReadDeniedContent(t *testing.T) {
	dir := diffTestRepo(t)
	a := newDiffApp(t, dir)
	a.settings = config.Settings{Permissions: config.PermissionRules{Deny: []string{"Read(a.txt)"}}}
	loadDiff(t, a)

	var f *workdiff.File
	for i := range a.diffState.snap.Files {
		if a.diffState.snap.Files[i].Path == "a.txt" {
			f = &a.diffState.snap.Files[i]
		}
	}
	if f == nil || !f.Hidden {
		t.Fatalf("a.txt not hidden: %+v", f)
	}
	for i := range a.diffState.snap.Files {
		a.diffState.selected = i
		a.diffState.linesFor = -1
		if v := a.diffView(); strings.Contains(v, "secret-new-text") {
			t.Fatalf("denied content on screen:\n%s", v)
		}
	}
}

func TestDiffReadDeniedMatchesRelativeAndAbsolute(t *testing.T) {
	p := permissions.From(nil, nil, []string{"Read(secrets/**)", "Read(/work/.env)"})
	if !diffReadDenied(p, "/work", "secrets/key.pem") {
		t.Error("relative deny not applied")
	}
	if !diffReadDenied(p, "/work", ".env") {
		t.Error("absolute deny not applied")
	}
	if diffReadDenied(p, "/work", "src/main.go") {
		t.Error("unrelated path hidden")
	}
}

func TestDiffPaneOutsideRepositoryAndClean(t *testing.T) {
	a := newDiffApp(t, t.TempDir())
	loadDiff(t, a)
	if v := a.diffView(); !strings.Contains(v, "not a git repository") {
		t.Fatalf("view:\n%s", v)
	}

	dir := diffTestRepo(t)
	cmd := exec.Command("git", "checkout", "-q", "--", ".")
	cmd.Dir = dir
	_ = cmd.Run()
	_ = os.Remove(filepath.Join(dir, "c.txt"))
	a = newDiffApp(t, dir)
	loadDiff(t, a)
	if v := a.diffView(); !strings.Contains(v, "clean") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestDiffStaleLoadIsDropped(t *testing.T) {
	a := newDiffApp(t, diffTestRepo(t))
	_ = a.push(viewDiff)
	a.diffState.gen++
	a.handleDiffMsg(workdiffLoadedMsg{gen: a.diffState.gen - 1, snap: workdiff.Snapshot{Files: []workdiff.File{{Path: "x"}}}})
	if len(a.diffState.snap.Files) != 0 {
		t.Fatal("stale result applied")
	}
}
