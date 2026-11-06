package tui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/fleet"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/trustgate"
)

// worktreeApp is an App in a trusted git repository that holds one worktree
// the harness made.
func worktreeApp(t *testing.T, w config.WorktreeSettings) (*App, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("BELAI_WORKTREES_DIR", t.TempDir())
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	repo := t.TempDir()
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
	if err := trustgate.Grant(repo, nil); err != nil {
		t.Fatal(err)
	}
	ws, err := fleet.PrepareWorktree(context.Background(), repo, kanban.Item{ID: "3f9a2c00-0000-4000-8000-0000000000d1", Title: "t"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.SaveRecord(false); err != nil {
		t.Fatal(err)
	}
	a := New(Options{Workdir: repo})
	a.width, a.height = 120, 40
	a.settings.Worktrees = &w
	a.settings.Sandbox = &config.SandboxSettings{Mode: "off"}
	store := kanban.Open(filepath.Join(t.TempDir(), "kanban"))
	a.kb = &kanbanUI{store: store, src: kanban.NewSource(kanban.ProvenanceFor(repo, a.sessionID, ""))}
	return a, ws.ID()
}

func answer(a *App, key string) tea.Msg {
	cmd := a.handleChatKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	if cmd == nil {
		return nil
	}
	return cmd()
}

func TestWorktreeCommandIsRegisteredWithItsVerbs(t *testing.T) {
	if got := strings.Join(worktreeVerbs, " "); got != "new list update drop" {
		t.Fatalf("verbs: %s", got)
	}
	a, _ := worktreeApp(t, config.WorktreeSettings{})
	a.handleCommand("/worktree")
	if !strings.Contains(lastSystemMessage(a), "usage: /worktree") {
		t.Fatalf("%q", lastSystemMessage(a))
	}
}

func TestWorktreeListShowsTheHarnessMadeWorktrees(t *testing.T) {
	a, id := worktreeApp(t, config.WorktreeSettings{})
	a.handleCommand("/worktree list")
	got := lastSystemMessage(a)
	if !strings.Contains(got, id) || !strings.Contains(got, "belai/K-3f9a2c/a1") || !strings.Contains(got, "present") {
		t.Fatalf("%q", got)
	}
}

func TestWorktreeNewFilesALabelledCard(t *testing.T) {
	a, _ := worktreeApp(t, config.WorktreeSettings{})
	a.handleCommand("/worktree new Add the \x1b[31mlogin page")
	got := lastSystemMessage(a)
	if !strings.Contains(got, "filed K-") || !strings.Contains(got, "belai kanban assign") {
		t.Fatalf("%q", got)
	}
	items, _ := a.kb.store.Search(kanban.Query{})
	if len(items) != 1 || strings.ContainsAny(items[0].Title, "\x1b") || !strings.Contains(strings.Join(items[0].Labels, ","), config.WorktreeLabel) {
		t.Fatalf("%+v", items)
	}
	a.handleCommand("/worktree new")
	if !strings.Contains(lastSystemMessage(a), "a title is required") {
		t.Fatalf("%q", lastSystemMessage(a))
	}
	a.kb = nil
	a.handleCommand("/worktree new anything")
	if !strings.Contains(lastSystemMessage(a), "kanban board is off") {
		t.Fatalf("%q", lastSystemMessage(a))
	}
}

func TestWorktreeDropConfirmsWithTheArgvThenRuns(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "torn-down")
	a, id := worktreeApp(t, config.WorktreeSettings{Teardown: [][]string{{"touch", marker}}})
	a.handleCommand("/worktree drop " + id)
	c := a.forgeConfirm
	if c.kind != forgeConfirmWorktreeDrop || c.wtID != id || !strings.Contains(c.prompt, "touch "+marker) {
		t.Fatalf("confirm: %+v", c)
	}
	if exists(marker) {
		t.Fatal("teardown ran before the answer")
	}
	// Anything but y cancels.
	if msg := answer(a, "n"); msg != nil || exists(marker) || a.forgeConfirm.kind != forgeConfirmNone {
		t.Fatalf("n: %v marker %v", msg, exists(marker))
	}
	a.handleCommand("/worktree drop " + id)
	msg, ok := answer(a, "y").(forgeActionMsg)
	if !ok || msg.err != nil || !strings.Contains(msg.text, "removed worktree "+id) || !exists(marker) {
		t.Fatalf("drop: %+v marker %v", msg, exists(marker))
	}
}

func TestWorktreeDropKeepsTheWorktreeWhenTeardownFails(t *testing.T) {
	a, id := worktreeApp(t, config.WorktreeSettings{Teardown: [][]string{{"false"}}})
	recs, _ := fleet.WorktreeRecords(a.workdir)
	a.handleCommand("/worktree drop " + id)
	msg := answer(a, "y").(forgeActionMsg)
	if msg.err == nil || msg.prefix != "worktree" || !strings.Contains(msg.err.Error(), "was kept") {
		t.Fatalf("%+v", msg)
	}
	if !exists(recs[0].Dir) {
		t.Fatal("the worktree was removed after a failed teardown")
	}
	a.handleForgeAction(msg)
	if got := lastSystemMessage(a); !strings.HasPrefix(got, "worktree: ") {
		t.Fatalf("%q", got)
	}
}

func TestWorktreeUpdateIsOffByDefaultAndConfirms(t *testing.T) {
	a, id := worktreeApp(t, config.WorktreeSettings{})
	a.handleCommand("/worktree update " + id)
	if a.forgeConfirm.kind != forgeConfirmNone || !strings.Contains(lastSystemMessage(a), "worktrees.update_mode") {
		t.Fatalf("update ran with the mode off: %+v %q", a.forgeConfirm, lastSystemMessage(a))
	}
	mode := "merge"
	a.settings.Worktrees.UpdateMode = &mode
	a.handleCommand("/worktree update " + id)
	if a.forgeConfirm.kind != forgeConfirmWorktreeUpdate || !strings.Contains(a.forgeConfirm.prompt, "merge") || !strings.Contains(a.forgeConfirm.prompt, "main") {
		t.Fatalf("%+v", a.forgeConfirm)
	}
	msg := answer(a, "y").(forgeActionMsg)
	if msg.err != nil || !strings.Contains(msg.text, "already up to date with main") {
		t.Fatalf("%+v", msg)
	}
}

func TestWorktreeActionsRefuseUnknownAndMalformedIDs(t *testing.T) {
	a, _ := worktreeApp(t, config.WorktreeSettings{})
	for _, arg := range []string{"drop", "drop ../x", "drop /etc", "drop K-aaaaaa-bbbbbb", "update K-aaaaaa-bbbbbb"} {
		a.handleCommand("/worktree " + arg)
		if a.forgeConfirm.kind != forgeConfirmNone {
			t.Errorf("%q opened a confirmation", arg)
		}
	}
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }
