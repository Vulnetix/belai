package gitsync

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/forge"
)

// g runs git in dir and fails the test on error; it returns trimmed stdout.
func g(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, dir, file, body, msg string) {
	t.Helper()
	write(t, dir, file, body)
	g(t, dir, "add", file)
	g(t, dir, "commit", "-m", msg)
}

// repos builds a bare origin with main, a clone of it to work in, and a second
// clone that stands for everyone else pushing to main.
func repos(t *testing.T) (work, other string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	g(t, root, "init", "--bare", "-b", "main", origin)
	seed := filepath.Join(root, "seed")
	g(t, root, "clone", origin, seed)
	g(t, seed, "checkout", "-b", "main")
	commit(t, seed, "a.txt", "a\n", "first")
	g(t, seed, "push", "-u", "origin", "main")
	g(t, origin, "symbolic-ref", "HEAD", "refs/heads/main")

	work = filepath.Join(root, "work")
	other = filepath.Join(root, "other")
	g(t, root, "clone", origin, work)
	g(t, root, "clone", origin, other)
	return work, other
}

func newHygiene(dir string) *Hygiene { return New(dir, true, forge.NonInteractiveRunner) }

func TestSyncPullsRebaseOnMain(t *testing.T) {
	work, other := repos(t)
	commit(t, other, "b.txt", "b\n", "upstream work")
	g(t, other, "push", "origin", "main")

	res := newHygiene(work).Sync(context.Background())
	if res.Outcome != OutcomeSynced || res.Branch != "main" || res.Base != "origin/main" || res.Pulled != 1 {
		t.Fatalf("result = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(work, "b.txt")); err != nil {
		t.Fatal("main did not pick up the upstream commit")
	}
	if res.NeedsPush {
		t.Fatal("a fast-forward of main needs no push")
	}
}

func TestSyncRebasesAFeatureBranchOntoUpstreamMain(t *testing.T) {
	work, other := repos(t)
	g(t, work, "checkout", "-b", "feature")
	commit(t, work, "feature.txt", "f\n", "feature work")
	g(t, work, "push", "-u", "origin", "feature")
	commit(t, other, "b.txt", "b\n", "upstream work")
	g(t, other, "push", "origin", "main")
	before := g(t, work, "rev-parse", "--short=7", "HEAD")

	res := newHygiene(work).Sync(context.Background())
	if res.Outcome != OutcomeSynced || res.Branch != "feature" || res.Pulled != 1 || res.From != before {
		t.Fatalf("result = %+v", res)
	}
	if !res.NeedsPush {
		t.Fatal("rewriting a pushed branch must say it needs a force push")
	}
	if got := g(t, work, "log", "--format=%s", "-3"); got != "feature work\nupstream work\nfirst" {
		t.Fatalf("history = %q, want the feature commit on top of upstream main", got)
	}
	if !strings.Contains(res.Line(), "force-with-lease") || !strings.Contains(res.Line(), res.From) {
		t.Fatalf("line = %q", res.Line())
	}
}

func TestSyncCurrentWhenNothingNew(t *testing.T) {
	work, _ := repos(t)
	if res := newHygiene(work).Sync(context.Background()); res.Outcome != OutcomeCurrent {
		t.Fatalf("result = %+v", res)
	}
}

func TestSyncAbortsAConflictAndLeavesTheBranchAlone(t *testing.T) {
	work, other := repos(t)
	g(t, work, "checkout", "-b", "feature")
	commit(t, work, "a.txt", "feature edit\n", "feature edits a")
	before := g(t, work, "rev-parse", "HEAD")
	commit(t, other, "a.txt", "upstream edit\n", "upstream edits a")
	g(t, other, "push", "origin", "main")

	res := newHygiene(work).Sync(context.Background())
	if res.Outcome != OutcomeConflict {
		t.Fatalf("result = %+v", res)
	}
	if after := g(t, work, "rev-parse", "HEAD"); after != before {
		t.Fatalf("HEAD moved from %s to %s", before, after)
	}
	if st := g(t, work, "status", "--porcelain"); st != "" {
		t.Fatalf("tree not clean after abort: %q", st)
	}
	if _, err := os.Stat(filepath.Join(work, ".git", "rebase-merge")); err == nil {
		t.Fatal("rebase left in progress")
	}
}

func TestSyncSkipsAnUnsafeRepository(t *testing.T) {
	work, other := repos(t)
	commit(t, other, "b.txt", "b\n", "upstream work")
	g(t, other, "push", "origin", "main")

	write(t, work, "a.txt", "local edit\n")
	if res := newHygiene(work).Sync(context.Background()); res.Outcome != OutcomeSkipped || res.Reason != "uncommitted changes" {
		t.Fatalf("dirty tree: %+v", res)
	}
	g(t, work, "checkout", "--", "a.txt")

	// Untracked files do not block: git itself refuses to overwrite them.
	write(t, work, "scratch.txt", "x\n")
	if res := newHygiene(work).Sync(context.Background()); res.Outcome != OutcomeSynced {
		t.Fatalf("untracked file: %+v", res)
	}

	g(t, work, "checkout", "--detach")
	if res := newHygiene(work).Sync(context.Background()); res.Outcome != OutcomeSkipped || res.Reason != "detached HEAD" {
		t.Fatalf("detached: %+v", res)
	}
	g(t, work, "checkout", "main")

	write(t, filepath.Join(work, ".git"), "MERGE_HEAD", g(t, work, "rev-parse", "HEAD")+"\n")
	if res := newHygiene(work).Sync(context.Background()); res.Outcome != OutcomeSkipped || !strings.Contains(res.Reason, "merge") {
		t.Fatalf("merge in progress: %+v", res)
	}
}

func TestSyncSkipsWithoutOrigin(t *testing.T) {
	dir := t.TempDir()
	g(t, dir, "init", "-b", "main")
	commit(t, dir, "a.txt", "a\n", "first")
	if res := newHygiene(dir).Sync(context.Background()); res.Outcome != OutcomeSkipped || res.Reason != "no origin remote" {
		t.Fatalf("result = %+v", res)
	}
}

func TestSyncWorksInALinkedWorktree(t *testing.T) {
	work, other := repos(t)
	wt := filepath.Join(filepath.Dir(work), "wt")
	g(t, work, "worktree", "add", "-b", "wt-branch", wt)
	commit(t, wt, "w.txt", "w\n", "worktree work")
	commit(t, other, "b.txt", "b\n", "upstream work")
	g(t, other, "push", "origin", "main")

	h := newHygiene(wt)
	if res := h.Sync(context.Background()); res.Outcome != OutcomeSynced || res.Branch != "wt-branch" {
		t.Fatalf("result = %+v", res)
	}
	if in := h.Info(context.Background(), true); in == nil || !in.Worktree || in.Worktrees != 2 || in.Branch != "wt-branch" {
		t.Fatalf("info = %+v", in)
	}
	if in := newHygiene(work).Info(context.Background(), true); in == nil || in.Worktree {
		t.Fatalf("main checkout reported as a worktree: %+v", in)
	}
}

func TestBeforeTurnRunsOnTheFirstTurnAndAfterCommits(t *testing.T) {
	work, other := repos(t)
	h := newHygiene(work)
	ctx := context.Background()
	push := func(file string) {
		commit(t, other, file, file+"\n", "upstream "+file)
		g(t, other, "push", "origin", "main")
	}

	push("one.txt")
	if res := h.BeforeTurn(ctx); res == nil || res.Outcome != OutcomeSynced {
		t.Fatalf("first turn: %+v", res)
	}
	push("two.txt")
	if res := h.BeforeTurn(ctx); res != nil {
		t.Fatalf("a turn with no commit since must not sync, got %+v", res)
	}
	commit(t, work, "mine.txt", "m\n", "my commit")
	if res := h.BeforeTurn(ctx); res == nil || res.Outcome != OutcomeSynced || res.Pulled != 1 {
		t.Fatalf("turn after a commit: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(work, "two.txt")); err != nil {
		t.Fatal("the sync after the commit did not bring in the newer upstream work")
	}
	if res := h.BeforeTurn(ctx); res != nil {
		t.Fatalf("nothing changed: %+v", res)
	}
}

func TestBeforeTurnRetriesASkippedFirstTurnAndReportsItOnce(t *testing.T) {
	work, other := repos(t)
	h := newHygiene(work)
	ctx := context.Background()
	commit(t, other, "b.txt", "b\n", "upstream work")
	g(t, other, "push", "origin", "main")

	write(t, work, "a.txt", "dirty\n")
	if res := h.BeforeTurn(ctx); res == nil || res.Outcome != OutcomeSkipped {
		t.Fatalf("dirty first turn: %+v", res)
	}
	if res := h.BeforeTurn(ctx); res != nil {
		t.Fatalf("the same skip twice: %+v", res)
	}
	g(t, work, "checkout", "--", "a.txt")
	if res := h.BeforeTurn(ctx); res == nil || res.Outcome != OutcomeSynced {
		t.Fatalf("clean second turn must still count as the first sync: %+v", res)
	}
}

func TestBeforeTurnIsSilentWhenOffOrOutsideARepo(t *testing.T) {
	work, other := repos(t)
	commit(t, other, "b.txt", "b\n", "upstream work")
	g(t, other, "push", "origin", "main")

	off := newHygiene(work)
	off.SetEnabled(false)
	if res := off.BeforeTurn(context.Background()); res != nil {
		t.Fatalf("disabled: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(work, "b.txt")); err == nil {
		t.Fatal("a disabled sync moved the branch")
	}
	if res := newHygiene(t.TempDir()).BeforeTurn(context.Background()); res != nil {
		t.Fatalf("not a repo: %+v", res)
	}
}

func TestInfoDescribesTheBranchAndBase(t *testing.T) {
	work, other := repos(t)
	g(t, work, "checkout", "-b", "feature")
	commit(t, work, "f.txt", "f\n", "feature work")
	commit(t, other, "b.txt", "b\n", "upstream work")
	g(t, other, "push", "origin", "main")
	g(t, work, "fetch", "origin")
	write(t, work, "a.txt", "dirty\n")

	h := newHygiene(work)
	in := h.Info(context.Background(), true)
	if in == nil || in.Branch != "feature" || in.Base != "origin/main" || in.AheadBase != 1 || in.BehindBase != 1 || !in.Dirty || in.Worktree || !in.Sync.Enabled || in.Head == "" {
		t.Fatalf("info = %+v", in)
	}
	if newHygiene(t.TempDir()).Info(context.Background(), true) != nil {
		t.Fatal("a directory outside a repository has no info")
	}
}

func TestSyncRunsNoRepositoryHooks(t *testing.T) {
	work, other := repos(t)
	g(t, work, "checkout", "-b", "feature")
	commit(t, work, "f.txt", "f\n", "feature work")
	marker := filepath.Join(t.TempDir(), "hook-ran")
	for _, hook := range []string{"pre-rebase", "post-rewrite", "post-checkout", "post-merge"} {
		body := "#!/bin/sh\ntouch " + marker + "\n"
		if err := os.WriteFile(filepath.Join(work, ".git", "hooks", hook), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	commit(t, other, "b.txt", "b\n", "upstream work")
	g(t, other, "push", "origin", "main")

	if res := newHygiene(work).Sync(context.Background()); res.Outcome != OutcomeSynced {
		t.Fatalf("result = %+v", res)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a repository hook ran outside the sandbox")
	}
}

func TestWatchPushesOnChangeOnly(t *testing.T) {
	work, _ := repos(t)
	h := newHygiene(work)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pushed := make(chan *Info, 8)
	go h.Watch(ctx, time.Hour, func(in *Info) { pushed <- in })

	first := <-pushed
	if first.Branch != "main" || !first.Sync.Enabled {
		t.Fatalf("first = %+v", first)
	}
	h.Kick()
	select {
	case in := <-pushed:
		t.Fatalf("nothing changed, yet pushed %+v", in)
	case <-time.After(300 * time.Millisecond):
	}
	h.SetEnabled(false)
	h.Kick()
	select {
	case in := <-pushed:
		if in.Sync.Enabled {
			t.Fatalf("switch change not reported: %+v", in)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the switch change was never pushed")
	}
}
