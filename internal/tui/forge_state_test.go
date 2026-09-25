package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/signet/internal/forge"
)

// forgeFake records every forge CLI call and answers from a script.
type forgeFake struct {
	mu    sync.Mutex
	out   map[string]string
	calls []string
}

func (f *forgeFake) run(_ context.Context, _ string, argv ...string) ([]byte, error) {
	key := strings.Join(argv, " ")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, key)
	if out, ok := f.out[key]; ok {
		return []byte(out), nil
	}
	return nil, errors.New("unscripted: " + key)
}

func (f *forgeFake) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func ghOnPath(string) (string, error) { return "/usr/bin/gh", nil }

// forgeApp returns an app with the runs panel open on the git tab and a
// snapshot already applied: branch feat on GitHub, two worktrees.
func forgeApp(t *testing.T, withPR bool) (*App, *forgeFake) {
	t.Helper()
	root := t.TempDir()
	a := New(Options{Workdir: root})
	a.width, a.height = 80, 30
	f := &forgeFake{out: map[string]string{}}
	a.forgeRunner = f.run
	a.forgeLook = ghOnPath
	rem, _ := forge.ParseRemote("https://token@github.com/vulnetix/signet.git")
	prov, _ := forge.For(rem, f.run, ghOnPath)
	snap := forge.Snapshot{
		Root:        root,
		Branch:      "feat",
		Upstream:    "origin/feat",
		LastSubject: "add the thing",
		Remote:      rem,
		Provider:    prov,
		Worktrees: []forge.Worktree{
			{Path: "/repo", Branch: "main", Head: "1111111"},
			{Path: root, Branch: "feat", Head: "2222222", Current: true},
			{Path: "/repo-wip", Branch: "wip", Head: "3333333", Dirty: true},
		},
	}
	if withPR {
		snap.PR = &forge.PR{Number: 42, Title: "Add the thing", State: "open", URL: "https://github.com/vulnetix/signet/pull/42"}
		snap.Checks = []forge.Check{
			{Name: "build", Workflow: "CI", State: forge.CheckPass, Link: "https://example/1"},
			{Name: "test", Workflow: "CI", State: forge.CheckFail, Link: "https://example/2"},
			{Name: "deploy", Workflow: "CD", State: forge.CheckPending},
		}
	}
	a.forge = forgeState{snap: snap, have: true, dir: root, probedAt: time.Now()}
	a.runsOpen, a.runsFocus, a.runsTab = true, true, tabGit
	return a, f
}

func TestForgeTabCycleShowsCIOnlyWithPR(t *testing.T) {
	a, _ := forgeApp(t, true)
	a.handleRunsPanelKey(tea.KeyMsg{Type: tea.KeyTab})
	if a.runsTab != tabCI {
		t.Fatalf("with a PR, tab after git must be ci, got %d", a.runsTab)
	}
	a.handleRunsPanelKey(tea.KeyMsg{Type: tea.KeyTab})
	if a.runsTab != tabActivity {
		t.Fatalf("ci must wrap to activity, got %d", a.runsTab)
	}
	if names := a.runsTabNames(); names[len(names)-1] != "ci" {
		t.Fatalf("ci missing from tab names: %v", names)
	}

	b, _ := forgeApp(t, false)
	b.handleRunsPanelKey(tea.KeyMsg{Type: tea.KeyTab})
	if b.runsTab != tabActivity {
		t.Fatalf("without a PR, ci must be skipped, got %d", b.runsTab)
	}
	if names := b.runsTabNames(); names[len(names)-1] != "git" {
		t.Fatalf("ci listed without a PR: %v", names)
	}
}

func TestForgeProbeFallsBackFromHiddenCI(t *testing.T) {
	a, _ := forgeApp(t, true)
	a.runsTab = tabCI
	snap := a.forge.snap
	snap.PR, snap.Checks = nil, nil
	a.handleForgeProbe(forgeProbeMsg{snap: snap, dir: a.forge.dir, at: time.Now()})
	if a.runsTab != tabGit {
		t.Fatalf("ci tab must fall back to git when the PR is gone, got %d", a.runsTab)
	}
}

func TestForgeNoProbeWhilePanelClosed(t *testing.T) {
	a, f := forgeApp(t, false)
	a.runsOpen = false
	if cmd := a.refreshForge(true); cmd != nil {
		t.Fatal("probe started with the panel closed")
	}
	a.runsOpen = true
	if cmd := a.refreshForge(false); cmd != nil {
		t.Fatal("fresh snapshot re-probed without force")
	}
	if len(f.called()) != 0 {
		t.Fatalf("unexpected calls %v", f.called())
	}
}

func TestForgeCacheSharedWithPanel(t *testing.T) {
	if New(Options{Workdir: t.TempDir()}).forgeCache != nil {
		t.Fatal("New must not create the forge cache: only Start probes")
	}
	a, f := forgeApp(t, false)
	a.forge = forgeState{}
	a.startForgeCache() // gitOK is false in the temp dir: no probe
	if a.forgeCache == nil {
		t.Fatal("startForgeCache did not create the cache")
	}
	a.forgeCache.Store(a.forgeDir(), forge.Snapshot{Root: a.workdir, Branch: "cached"}, time.Now())
	if cmd := a.refreshForge(false); cmd != nil {
		t.Fatal("a fresh cached snapshot was re-probed")
	}
	if !a.forge.have || a.forge.snap.Branch != "cached" {
		t.Fatalf("panel did not adopt the cached snapshot: %+v", a.forge)
	}
	// The panel's own probe lands in the cache for the session to use.
	a.handleForgeProbe(forgeProbeMsg{snap: forge.Snapshot{Root: a.workdir, Branch: "probed"}, dir: a.forgeDir(), at: time.Now().Add(time.Second)})
	if s, _, _ := a.forgeCache.Get(a.forgeDir()); s.Branch != "probed" {
		t.Fatalf("probe not stored in the shared cache: %+v", s)
	}
	if len(f.called()) != 0 {
		t.Fatalf("unexpected calls %v", f.called())
	}
}

func TestForgeRemoveGuards(t *testing.T) {
	a, f := forgeApp(t, false)
	// current worktree: refused, no confirmation
	a.runsSel = 1
	a.handleRunsGitKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if a.forgeConfirm.kind != forgeConfirmNone {
		t.Fatal("removal of the current worktree reached a confirmation")
	}
	// main worktree: refused
	a.runsSel = 0
	a.handleRunsGitKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if a.forgeConfirm.kind != forgeConfirmNone {
		t.Fatal("removal of the main worktree reached a confirmation")
	}
	// dirty worktree: force confirmation
	a.runsSel = 2
	a.handleRunsGitKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if a.forgeConfirm.kind != forgeConfirmRemoveForce || !strings.Contains(a.forgeConfirm.prompt, "force") {
		t.Fatalf("dirty worktree: %+v", a.forgeConfirm)
	}
	// anything but y cancels without running git
	if cmd := a.handleChatKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")}); cmd != nil {
		t.Fatal("n returned a command")
	}
	if a.forgeConfirm.kind != forgeConfirmNone || len(f.called()) != 0 {
		t.Fatalf("cancel left state %+v calls %v", a.forgeConfirm, f.called())
	}
	// y runs the forced removal
	a.handleRunsGitKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	f.out["git worktree remove --force /repo-wip"] = ""
	cmd := a.handleChatKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if cmd == nil {
		t.Fatal("y returned no command")
	}
	if msg, ok := cmd().(forgeActionMsg); !ok || msg.err != nil {
		t.Fatalf("remove: %+v", msg)
	}
}

func TestForgeCreatePRAlwaysConfirms(t *testing.T) {
	for _, guardrails := range []bool{true, false} {
		a, f := forgeApp(t, false)
		if !guardrails {
			a.guardrailsOverride = boolPtr(false)
		}
		if a.guardrailsEnabled() != guardrails {
			t.Fatalf("guardrails not set to %v", guardrails)
		}
		a.handleRunsGitKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
		if a.forgeInput.kind != forgeInputPRTitle || a.editor.Value() != "add the thing" {
			t.Fatalf("title prompt: %+v %q", a.forgeInput, a.editor.Value())
		}
		a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})
		if a.forgeInput.kind != forgeInputPRBody {
			t.Fatalf("body prompt: %+v", a.forgeInput)
		}
		a.editor.SetValue("details")
		a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})
		c := a.forgeConfirm
		if c.kind != forgeConfirmCreatePR || !strings.Contains(c.prompt, "vulnetix/signet") || !strings.Contains(c.prompt, "feat") {
			t.Fatalf("guardrails=%v: confirm %+v", guardrails, c)
		}
		if len(f.called()) != 0 {
			t.Fatalf("CLI ran before confirmation: %v", f.called())
		}
		f.out["gh pr create --head feat --title add the thing --body details"] = "https://github.com/vulnetix/signet/pull/43"
		cmd := a.handleChatKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
		msg, _ := cmd().(forgeActionMsg)
		if msg.err != nil || !strings.Contains(msg.text, "pull/43") {
			t.Fatalf("create: %+v", msg)
		}
	}
}

func TestForgeCreatePRPushesFirstWithoutUpstream(t *testing.T) {
	a, f := forgeApp(t, false)
	a.forge.snap.Upstream = ""
	a.askCreatePR(forge.CreatePRArgs{Branch: "feat", Title: "t"})
	if a.forgeConfirm.kind != forgeConfirmPush {
		t.Fatalf("expected push confirm, got %+v", a.forgeConfirm)
	}
	f.out["git push -u origin feat"] = ""
	msg := a.handleChatKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})().(forgeActionMsg)
	a.handleForgeAction(msg)
	if a.forgeConfirm.kind != forgeConfirmCreatePR {
		t.Fatalf("push must chain to the create confirm, got %+v", a.forgeConfirm)
	}
}

func TestForgeAddWorktreeOutsideRootsRefused(t *testing.T) {
	a, f := forgeApp(t, false)
	outside := filepath.Join(os.TempDir(), "signet-outside-worktree")
	if cmd := a.addWorktree("newb " + outside); cmd != nil {
		t.Fatal("outside path produced a command")
	}
	if len(f.called()) != 0 {
		t.Fatalf("git ran: %v", f.called())
	}
	if cmd := a.addWorktree("newb"); cmd == nil {
		t.Fatal("default path under the root was refused")
	}
}

func TestForgeTabsGolden(t *testing.T) {
	for _, tc := range []struct {
		name string
		tab  int
		w    int
	}{
		{"git_80", tabGit, 80},
		{"git_40", tabGit, 40},
		{"ci_80", tabCI, 80},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := forgeApp(t, true)
			a.width = tc.w
			a.runsTab = tc.tab
			a.runsSel = 0
			// The temp root differs per run; pin it for a stable golden.
			a.forge.snap.Worktrees[1].Path = "/repo-feat"
			got := ansi.Strip(a.renderRunsPanel())
			if lines := strings.Count(got, "\n"); lines != a.runsPanelHeight() {
				t.Fatalf("rendered %d lines, height says %d", lines, a.runsPanelHeight())
			}
			for _, line := range strings.Split(got, "\n") {
				if ansi.StringWidth(line) > a.contentWidth() {
					t.Fatalf("line wider than %d: %q", a.contentWidth(), line)
				}
			}
			path := filepath.Join("testdata", "forge_"+tc.name+".golden")
			if os.Getenv("UPDATE_GOLDEN") != "" {
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden (run UPDATE_GOLDEN=1 to create): %v", err)
			}
			if strings.TrimSpace(got) != strings.TrimSpace(string(want)) {
				t.Fatalf("mismatch:\n%s", diffLines(string(want), got))
			}
		})
	}
}
