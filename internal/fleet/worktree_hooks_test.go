package fleet

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/sandbox"
	"github.com/vulnetix/belai/internal/testrun"
)

const hookItem = "3f9a2c00-0000-4000-8000-0000000000b1"

// hookWorkspace makes a real worktree with the given hook settings and no OS
// sandbox, so the argv run directly.
func hookWorkspace(t *testing.T, w config.WorktreeSettings) (*Workspace, string) {
	t.Helper()
	testEnv(t)
	repo := gitRepo(t)
	ws, err := PrepareWorktree(context.Background(), repo, kanban.Item{ID: hookItem, Title: "t"}, "")
	if err != nil {
		t.Fatal(err)
	}
	ws.UseHooks(WorktreeHooks{
		Settings: config.Settings{Worktrees: &w},
		Policy:   func(string) sandbox.Policy { return sandbox.Policy{} },
	})
	return ws, repo
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestSetupRunsInOrderInTheWorktreeWithoutAShell(t *testing.T) {
	ws, _ := hookWorkspace(t, config.WorktreeSettings{Setup: [][]string{
		{"touch", "first"},
		// A shell would split this at the semicolon and run the second word;
		// without one it is a single file name.
		{"touch", "second;touch third"},
	}})
	if err := ws.RunSetup(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first", "second;touch third"} {
		if !exists(filepath.Join(ws.Dir, name)) {
			t.Errorf("%q was not made in the worktree", name)
		}
	}
	if exists(filepath.Join(ws.Dir, "third")) {
		t.Error("an argv word was run by a shell")
	}
}

func TestSetupFailureStopsAndReportsOnlyHarnessText(t *testing.T) {
	ws, _ := hookWorkspace(t, config.WorktreeSettings{Setup: [][]string{
		{"sh", "-c", "echo SECRET-OUTPUT-LINE >&2; exit 7"},
		{"touch", "never"},
	}})
	err := ws.RunSetup(context.Background())
	he, ok := AsHookError(err)
	if !ok {
		t.Fatalf("want a *HookError, got %v", err)
	}
	if he.Phase != "setup" || he.Index != 0 || he.ExitCode != 7 || he.Program != "sh" {
		t.Fatalf("%+v", he)
	}
	if strings.Contains(he.Error(), "SECRET-OUTPUT") {
		t.Fatalf("the error carries process output: %q", he.Error())
	}
	if !strings.Contains(he.Detail(), "SECRET-OUTPUT-LINE") {
		t.Fatalf("the detail lost the output: %q", he.Detail())
	}
	if exists(filepath.Join(ws.Dir, "never")) {
		t.Fatal("setup went on after a failure")
	}
}

func TestFailedSetupDiscardsWithoutTeardown(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "teardown-ran")
	ws, _ := hookWorkspace(t, config.WorktreeSettings{
		Setup:    [][]string{{"false"}},
		Teardown: [][]string{{"touch", marker}},
	})
	if err := ws.RunSetup(context.Background()); err == nil {
		t.Fatal("setup passed")
	}
	if err := ws.Discard(context.Background()); err != nil {
		t.Fatal(err)
	}
	if exists(ws.Dir) {
		t.Fatal("a worktree whose setup failed was kept")
	}
	if exists(marker) {
		t.Fatal("teardown ran for a worktree whose setup did not finish")
	}
}

func TestTeardownRunsBeforeRemoval(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "teardown-ran")
	ws, _ := hookWorkspace(t, config.WorktreeSettings{Teardown: [][]string{{"touch", marker}}})
	ctx := context.Background()
	if err := ws.RunSetup(ctx); err != nil {
		t.Fatal(err)
	}
	if err := ws.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	if !exists(marker) || exists(ws.Dir) {
		t.Fatalf("teardown ran %v, worktree present %v", exists(marker), exists(ws.Dir))
	}
}

func TestTeardownFailureKeepsTheWorktree(t *testing.T) {
	ws, repo := hookWorkspace(t, config.WorktreeSettings{Teardown: [][]string{{"false"}}})
	ctx := context.Background()
	if err := ws.RunSetup(ctx); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(ws.Dir, "work.txt"), []byte("x\n"), 0o644)
	err := ws.Discard(ctx)
	if he, ok := AsHookError(err); !ok || he.Phase != "teardown" {
		t.Fatalf("want a teardown *HookError, got %v", err)
	}
	if !exists(filepath.Join(ws.Dir, "work.txt")) {
		t.Fatal("a failed teardown still deleted the worktree")
	}
	if out, _ := exec.Command("git", "-C", repo, "branch", "--list", ws.Branch).CombinedOutput(); !strings.Contains(string(out), ws.Branch) {
		t.Fatal("a failed teardown deleted the branch")
	}
	// Once the teardown passes the worktree goes.
	ws.hooks.Settings.Worktrees.Teardown = [][]string{{"true"}}
	if err := ws.Remove(ctx); err != nil || exists(ws.Dir) {
		t.Fatalf("remove after a passing teardown: %v, present %v", err, exists(ws.Dir))
	}
}

func TestTeardownDoesNotRunInATamperedWorktree(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "teardown-ran")
	ws, _ := hookWorkspace(t, config.WorktreeSettings{Teardown: [][]string{{"touch", marker}}})
	ctx := context.Background()
	if err := ws.RunSetup(ctx); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(ws.Dir, ".git"), []byte("gitdir: /tmp/evil\n"), 0o644)
	_ = ws.Remove(ctx)
	if exists(marker) {
		t.Fatal("the user's argv ran in a worktree whose .git file was rewritten")
	}
	if exists(ws.Dir) {
		t.Fatal("a tampered worktree was left behind")
	}
}

func TestSetupThatRewritesTheGitFileFails(t *testing.T) {
	ws, _ := hookWorkspace(t, config.WorktreeSettings{Setup: [][]string{{"sh", "-c", "echo 'gitdir: /tmp/evil' > .git"}}})
	if err := ws.RunSetup(context.Background()); err == nil {
		t.Fatal("setup that rewrote .git passed")
	}
	if ws.ready {
		t.Fatal("a tampered worktree was marked ready")
	}
}

func TestSetupTimeoutIsEnforced(t *testing.T) {
	to := 1
	ws, _ := hookWorkspace(t, config.WorktreeSettings{Setup: [][]string{{"sleep", "10"}}, TimeoutSeconds: &to})
	err := ws.RunSetup(context.Background())
	he, ok := AsHookError(err)
	if !ok || he.Status != testrun.TimedOut {
		t.Fatalf("want a timeout, got %v", err)
	}
}

func TestSetupOutputIsNeverCommitted(t *testing.T) {
	ws, _ := hookWorkspace(t, config.WorktreeSettings{Setup: [][]string{{"sh", "-c", "mkdir deps && echo x > deps/lib.txt"}}})
	ctx := context.Background()
	if err := ws.RunSetup(ctx); err != nil {
		t.Fatal(err)
	}
	// Work the model did afterwards is committed; what setup left is not.
	os.WriteFile(filepath.Join(ws.Dir, "work.txt"), []byte("x\n"), 0o644)
	n, err := ws.Commit(ctx, "belai: test")
	if err != nil || n != 1 {
		t.Fatalf("commit: %d %v", n, err)
	}
	out, _ := exec.Command("git", "-C", ws.Dir, "ls-files").CombinedOutput()
	if strings.Contains(string(out), "deps/") {
		t.Fatalf("setup output was committed:\n%s", out)
	}
}

func TestPermissionDenyRuleRefusesAnArgv(t *testing.T) {
	ws, _ := hookWorkspace(t, config.WorktreeSettings{Setup: [][]string{{"touch", "blocked"}}})
	ws.hooks.Settings.Permissions.Deny = []string{"Bash(touch*)"}
	err := ws.RunSetup(context.Background())
	if he, ok := AsHookError(err); !ok || he.Status != testrun.Denied {
		t.Fatalf("a deny rule did not refuse the argv: %v", err)
	}
	if exists(filepath.Join(ws.Dir, "blocked")) {
		t.Fatal("a denied argv ran")
	}
}

func TestAnAskRuleCannotBeAnsweredHere(t *testing.T) {
	ws, _ := hookWorkspace(t, config.WorktreeSettings{Setup: [][]string{{"touch", "asked"}}})
	ws.hooks.Settings.Permissions.Ask = []string{"Bash(touch*)"}
	err := ws.RunSetup(context.Background())
	if he, ok := AsHookError(err); !ok || he.Status != testrun.NeedsApproval {
		t.Fatalf("an ask rule did not stop the argv: %v", err)
	}
}

func TestSandboxPolicyCutsTheNetworkUnlessAllowed(t *testing.T) {
	dir := t.TempDir()
	h := WorktreeHooks{Settings: config.Settings{}}
	p := h.sandboxPolicy(dir)
	if p.Mode == sandbox.ModeOff {
		t.Fatal("the default policy is off")
	}
	if !p.DenyNetwork {
		t.Fatal("the network is not denied by default")
	}
	if !sandbox.NetworkDenyEnforced() && p.Mode != sandbox.ModeRequired {
		t.Fatalf("a cut that cannot be enforced must refuse the run, got mode %q", p.Mode)
	}
	found := false
	for _, w := range p.Writable {
		if w == dir {
			found = true
		}
	}
	if !found {
		t.Fatalf("the worktree is not a writable root: %v", p.Writable)
	}
	hidden := false
	if g, err := config.GlobalDir(); err == nil {
		for _, hp := range p.Hidden {
			hidden = hidden || hp == g
		}
		if !hidden {
			t.Fatalf("Belai's state directory is not hidden: %v", p.Hidden)
		}
	}
	allow := "allow"
	h.Settings.Worktrees = &config.WorktreeSettings{Network: &allow}
	if h.sandboxPolicy(dir).DenyNetwork {
		t.Fatal("worktrees.network allow did not lift the cut")
	}
}

func TestSandboxPolicyIsOffWithGuardrailsOff(t *testing.T) {
	// The same switch that turns the sandbox off for Bash turns it off here, and
	// the network rule is then not applied (nothing could enforce it).
	h := WorktreeHooks{Settings: config.Settings{Sandbox: &config.SandboxSettings{Mode: "off"}}}
	if p := h.sandboxPolicy(t.TempDir()); p.Mode != sandbox.ModeOff {
		t.Fatalf("mode %q", p.Mode)
	}
}

func TestHookRunnerReceivesTheDerivedPolicy(t *testing.T) {
	var got sandbox.Policy
	ws, _ := hookWorkspace(t, config.WorktreeSettings{Setup: [][]string{{"true"}}})
	ws.hooks.Policy = nil
	ws.hooks.Run = func(ctx context.Context, opts testrun.Options, name string, argv []string) testrun.Result {
		got = sandbox.FromContext(ctx)
		if opts.Dir != ws.Dir {
			t.Errorf("ran in %q, want %q", opts.Dir, ws.Dir)
		}
		return testrun.Result{Status: testrun.Passed}
	}
	if err := ws.RunSetup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !got.DenyNetwork {
		t.Fatalf("the runner's context carries no network cut: %+v", got)
	}
}

func TestRunSetupIsANoOpWithoutHooksOrAWorktree(t *testing.T) {
	if err := SharedWorkspace(t.TempDir()).RunSetup(context.Background()); err != nil {
		t.Fatal(err)
	}
	ws, _ := hookWorkspace(t, config.WorktreeSettings{})
	if err := ws.RunSetup(context.Background()); err != nil || !ws.ready {
		t.Fatalf("%v ready=%v", err, ws.ready)
	}
}

func TestHookErrorWordsNeverHoldOutput(t *testing.T) {
	he := &HookError{Phase: "teardown", Index: 1, Program: "make", Status: testrun.Errored, Note: "could not start: command not found", output: "RAW"}
	if got := he.Error(); strings.Contains(got, "RAW") || !strings.Contains(got, "teardown command 2 (make)") {
		t.Fatalf("%q", got)
	}
	if _, ok := AsHookError(errors.New("x")); ok {
		t.Fatal("a plain error was taken for a hook error")
	}
}
