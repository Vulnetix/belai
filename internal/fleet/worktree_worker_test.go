package fleet

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/run"
)

func worktreeWorker(t *testing.T, ws config.WorktreeSettings, isolation string, runner TurnRunner) (*kanban.Store, kanban.Item, *Worker) {
	t.Helper()
	store, reg := testEnv(t)
	pinGit(t)
	it, _, _ := store.Add(kanban.ItemInput{Title: "do it", Labels: []string{"build", config.WorktreeLabel}}, kanban.Provenance{})
	p := builderProfile()
	if isolation != "" {
		p.Workspace = &agentprofile.WorkspaceSpec{Isolation: isolation}
	}
	w := newWorker(t, store, reg, p, runner)
	w.Repo = gitRepo(t)
	w.Settings.Worktrees = &ws
	return store, it, w
}

func worktreeDirs(t *testing.T) []string {
	t.Helper()
	root, err := config.WorktreesDir()
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	keys, _ := os.ReadDir(root)
	for _, k := range keys {
		subs, _ := os.ReadDir(filepath.Join(root, k.Name()))
		for _, s := range subs {
			dirs = append(dirs, filepath.Join(root, k.Name(), s.Name()))
		}
	}
	return dirs
}

func TestASetupFailureKeepsOutputOffTheBoard(t *testing.T) {
	var turns []Turn
	store, it, w := worktreeWorker(t, config.WorktreeSettings{
		Setup: [][]string{{"sh", "-c", "echo LEAKY-SETUP-OUTPUT >&2; exit 3"}},
	}, agentprofile.IsolationWorktree, func(ctx context.Context, tt Turn) (run.Result, error) {
		turns = append(turns, tt)
		return run.Result{StopReason: run.StopComplete, Passes: 1}, nil
	})
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(it.ID)
	if got.List != kanban.Backlog || got.Attempts != 1 {
		t.Fatalf("%s attempts=%d note=%q", got.List, got.Attempts, got.LastNote())
	}
	if note := got.LastNote(); !strings.Contains(note, "worktree setup command 1 (sh) exited 3") || strings.Contains(note, "LEAKY") {
		t.Fatalf("the note is not harness text: %q", note)
	}
	// The setup-debug turn is the only place the output goes, as a classified
	// attachment, and it runs in the trusted repository.
	if len(turns) != 1 || turns[0].Workspace != nil || !strings.Contains(turns[0].Setup, "LEAKY-SETUP-OUTPUT") {
		t.Fatalf("turns: %+v", turns)
	}
	if dirs := worktreeDirs(t); len(dirs) != 0 {
		t.Fatalf("a worktree whose setup failed was left: %v", dirs)
	}
}

func TestAWorktreeCardGetsAWorktreeAndIsKept(t *testing.T) {
	var ran Turn
	store, it, w := worktreeWorker(t, config.WorktreeSettings{
		Setup: [][]string{{"touch", "ready"}},
	}, "" /* the profile asks for no isolation */, func(ctx context.Context, tt Turn) (run.Result, error) {
		ran = tt
		if tt.Workspace == nil || !tt.Workspace.Worktree || !exists(filepath.Join(tt.Workdir, "ready")) {
			t.Errorf("no prepared worktree: %+v", tt.Workspace)
		}
		return complete(ctx, tt)
	})
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(it.ID)
	if got.Branch == "" {
		t.Fatalf("no branch recorded: %+v", got)
	}
	if !exists(ran.Workdir) {
		t.Fatal("a /worktree session's directory was removed (keep_on_exit defaults on)")
	}
	recs, err := WorktreeRecords(w.Repo)
	if err != nil || len(recs) != 1 || !recs[0].Kept || recs[0].Dir != ran.Workdir {
		t.Fatalf("records: %+v %v", recs, err)
	}
}

func TestKeepOnExitOffRemovesTheWorktreeAndItsRecord(t *testing.T) {
	off := false
	var dir string
	_, _, w := worktreeWorker(t, config.WorktreeSettings{KeepOnExit: &off}, "", func(ctx context.Context, tt Turn) (run.Result, error) {
		dir = tt.Workdir
		return complete(ctx, tt)
	})
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if dir == "" || exists(dir) {
		t.Fatalf("the worktree %q was kept with keep_on_exit off", dir)
	}
	if recs, _ := WorktreeRecords(w.Repo); len(recs) != 0 {
		t.Fatalf("record survived: %+v", recs)
	}
}

func TestAResumedBranchIsUpdatedBeforeTheTurn(t *testing.T) {
	var sawTheirs []bool
	first := true
	store, it, w := worktreeWorker(t, config.WorktreeSettings{}, agentprofile.IsolationWorktree, func(ctx context.Context, tt Turn) (run.Result, error) {
		sawTheirs = append(sawTheirs, exists(filepath.Join(tt.Workdir, "theirs.txt")))
		if first {
			first = false
			os.WriteFile(filepath.Join(tt.Workdir, "progress.txt"), []byte("step\n"), 0o644)
			return run.Result{StopReason: run.StopMaxPasses, Passes: 8}, nil
		}
		if !strings.Contains(workspaceDirective(tt.Workspace, false, agentprofile.PublishNone), "brought the branch up to date") {
			t.Errorf("the workspace note does not say the branch was updated")
		}
		return complete(ctx, tt)
	})
	// Not a /worktree card: this only tests the resume path.
	store.Route(it.ID, kanban.RoutePatch{Labels: &[]string{"build"}}, "")
	w.Run(context.Background())
	commitFile(t, w.Repo, "theirs.txt", "theirs\n", "feat: theirs")
	mode := config.WorktreeUpdateMerge
	w.Settings.Worktrees.UpdateMode = &mode
	got := runOnce(t, w, store, it.ID)
	if len(sawTheirs) != 2 || sawTheirs[0] || !sawTheirs[1] {
		t.Fatalf("theirs.txt seen per turn: %v (card %s %q)", sawTheirs, got.List, got.LastNote())
	}
}

func TestAConflictingUpdateLeavesTheResumedBranchAsItWas(t *testing.T) {
	first := true
	var heads []string
	store, it, w := worktreeWorker(t, config.WorktreeSettings{}, agentprofile.IsolationWorktree, func(ctx context.Context, tt Turn) (run.Result, error) {
		heads = append(heads, gitIn(t, tt.Workdir, "rev-parse", "HEAD"))
		if first {
			first = false
			os.WriteFile(filepath.Join(tt.Workdir, "README"), []byte("mine\n"), 0o644)
			return run.Result{StopReason: run.StopMaxPasses, Passes: 8}, nil
		}
		if st := gitIn(t, tt.Workdir, "status", "--porcelain", "--untracked-files=no"); st != "" {
			t.Errorf("the aborted update left changes:\n%s", st)
		}
		return complete(ctx, tt)
	})
	store.Route(it.ID, kanban.RoutePatch{Labels: &[]string{"build"}}, "")
	w.Run(context.Background())
	commitFile(t, w.Repo, "README", "theirs\n", "feat: theirs")
	mode := config.WorktreeUpdateRebase
	w.Settings.Worktrees.UpdateMode = &mode
	runOnce(t, w, store, it.ID)
	if len(heads) != 2 {
		t.Fatalf("%d turns", len(heads))
	}
}
