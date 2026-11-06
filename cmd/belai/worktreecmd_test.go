package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/fleet"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/sandbox"
)

// worktreeRepo makes a trusted git repository with one harness-made worktree
// and its record, and runs the test from inside it.
func worktreeRepo(t *testing.T, settings config.Settings) (repo, id string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("BELAI_WORKTREES_DIR", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	repo = t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "README"), []byte("hi\n"), 0o644)
	git("add", "README")
	git("commit", "--no-gpg-sign", "-q", "-m", "init")
	if err := config.SaveProject(repo, settings); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveGlobal(settings); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	if err := trustRepo(repo, true); err != nil {
		t.Fatal(err)
	}
	ws, err := fleet.PrepareWorktree(context.Background(), repo, kanban.Item{ID: "3f9a2c00-0000-4000-8000-0000000000c1", Title: "t"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.SaveRecord(false); err != nil {
		t.Fatal(err)
	}
	return repo, ws.ID()
}

func runWT(t *testing.T, interactive bool, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := runWorktreeCLI(context.Background(), args, strings.NewReader(stdin), interactive, &out, &errb)
	return code, out.String(), errb.String()
}

func TestWorktreeCLIListsTheWorktrees(t *testing.T) {
	_, id := worktreeRepo(t, config.Settings{})
	code, out, errOut := runWT(t, false, "", "list")
	if code != 0 || !strings.Contains(out, id) || !strings.Contains(out, "belai/K-3f9a2c/a1") {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
	code, out, _ = runWT(t, false, "", "list", "-json")
	if code != 0 || !strings.Contains(out, `"id": "`+id+`"`) || !strings.Contains(out, `"exists": true`) {
		t.Fatalf("%d %q", code, out)
	}
}

func TestWorktreeCLIFilesACard(t *testing.T) {
	worktreeRepo(t, config.Settings{})
	code, out, errOut := runWT(t, false, "", "new", "-label", "build", "Add \x1b[31mthe\x00 login page")
	if code != 0 || !strings.Contains(out, "filed with the worktree label") {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
	store, err := kanban.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	items, _ := store.Search(kanban.Query{})
	var found bool
	for _, it := range items {
		if strings.Contains(it.Title, "login page") {
			found = true
			if strings.ContainsAny(it.Title, "\x1b\x00") || !strings.Contains(strings.Join(it.Labels, ","), "worktree") {
				t.Fatalf("%+v", it)
			}
		}
	}
	if !found {
		t.Fatal("the card was not filed")
	}
	if code, _, _ := runWT(t, false, "", "new"); code != 2 {
		t.Fatalf("a missing title exited %d", code)
	}
	if code, _, e := runWT(t, false, "", "new", "-agent", "no-such-profile", "Another card"); code == 0 {
		t.Fatalf("an unknown profile was accepted: %q", e)
	}
}

func TestWorktreeCLIDropAsksFirstAndShowsTheArgv(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "torn-down")
	s := config.Settings{Sandbox: &config.SandboxSettings{Mode: "off"}, Worktrees: &config.WorktreeSettings{Teardown: [][]string{{"touch", marker}}}}
	_, id := worktreeRepo(t, s)

	// No terminal and no -yes: refused, nothing runs.
	code, _, errOut := runWT(t, false, "", "drop", id)
	if code == 0 || !strings.Contains(errOut, "no terminal") || exists(marker) {
		t.Fatalf("%d %q", code, errOut)
	}
	// A terminal, answered no.
	code, _, errOut = runWT(t, true, "n\n", "drop", id)
	if code == 0 || !strings.Contains(errOut, "touch "+marker) || exists(marker) {
		t.Fatalf("%d %q", code, errOut)
	}
	// Answered yes: the argv was shown, then ran, and the worktree is gone.
	code, out, errOut := runWT(t, true, "y\n", "drop", id)
	if code != 0 || !strings.Contains(errOut, "touch "+marker) || !exists(marker) || !strings.Contains(out, "removed worktree "+id) {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
}

func TestWorktreeCLIUpdateNeedsAMode(t *testing.T) {
	_, id := worktreeRepo(t, config.Settings{})
	code, _, errOut := runWT(t, false, "", "update", "-yes", id)
	if code == 0 || !strings.Contains(errOut, "update_mode") {
		t.Fatalf("%d %q", code, errOut)
	}
	mode := "merge"
	_, id = worktreeRepo(t, config.Settings{Worktrees: &config.WorktreeSettings{UpdateMode: &mode}})
	code, out, errOut := runWT(t, false, "", "update", "-yes", id)
	if code != 0 || !strings.Contains(out, "already up to date with main") {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
}

func TestWorktreeCLIRefusesUnknownIDsAndUsage(t *testing.T) {
	worktreeRepo(t, config.Settings{})
	for _, args := range [][]string{{"drop"}, {"drop", "../x"}, {"drop", "K-aaaaaa-bbbbbb", "extra"}, {"update", "/etc"}} {
		if code, _, _ := runWT(t, true, "y\n", args...); code == 0 {
			t.Errorf("%v succeeded", args)
		}
	}
	if code, _, e := runWT(t, true, "y\n", "drop", "K-aaaaaa-bbbbbb"); code == 0 || !strings.Contains(e, "no record") {
		t.Errorf("an unknown id: %d %q", code, e)
	}
	if code, _, _ := runWT(t, false, "", "frobnicate"); code != 2 {
		t.Errorf("unknown command exited %d", code)
	}
	if code, _, e := runWT(t, false, "", "help"); code != 0 || !strings.Contains(e, "usage: belai worktree") {
		t.Errorf("help: %d %q", code, e)
	}
}

func TestWorktreeCLIRefusesInsideTheSandbox(t *testing.T) {
	t.Setenv(sandbox.EnvMarker, "1")
	if code, _, e := runWT(t, false, "", "list"); code == 0 || !strings.Contains(e, "not reachable from inside Belai's sandbox") {
		t.Fatalf("%d %q", code, e)
	}
}

func TestWorktreeIsListedAndDocumentedInHelp(t *testing.T) {
	found := false
	for _, c := range topCommands {
		found = found || c.name == "worktree"
	}
	if !found {
		t.Fatal("belai worktree is not in the command list")
	}
	for _, want := range []string{"new", "list", "update", "drop", "-yes", "-agent", "-trust-dir", "worktrees.teardown"} {
		if !strings.Contains(worktreeUsage, want) {
			t.Errorf("usage lacks %q", want)
		}
	}
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }
