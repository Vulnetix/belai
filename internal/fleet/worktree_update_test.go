package fleet

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/kanban"
)

func commitFile(t *testing.T, dir, name, body, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", name)
	gitIn(t, dir, "commit", "--no-gpg-sign", "-q", "-m", msg)
}

// updateWorkspace makes a worktree whose branch has one commit of its own.
func updateWorkspace(t *testing.T) (*Workspace, string) {
	t.Helper()
	testEnv(t)
	pinGit(t)
	repo := gitRepo(t)
	ws, err := PrepareWorktree(context.Background(), repo, kanban.Item{ID: hookItem, Title: "t"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if ws.BaseRef != "main" {
		t.Fatalf("BaseRef %q, want main", ws.BaseRef)
	}
	commitFile(t, ws.Dir, "mine.txt", "mine\n", "feat: mine")
	return ws, repo
}

func TestUpdateFromBaseMergesAndMovesBaseOnlyAfterSuccess(t *testing.T) {
	ws, repo := updateWorkspace(t)
	oldBase := ws.Base
	commitFile(t, repo, "theirs.txt", "theirs\n", "feat: theirs")
	tip := gitIn(t, repo, "rev-parse", "main")

	res, err := ws.UpdateFromBase(context.Background(), config.WorktreeUpdateMerge)
	if err != nil {
		t.Fatal(err)
	}
	if res.UpToDate || res.Brought != 1 || res.Commit != tip || res.Mode != "merge" {
		t.Fatalf("%+v", res)
	}
	if ws.Base != tip || ws.Base == oldBase || ws.Updated != tip || ws.UpdatedBy != "merge" {
		t.Fatalf("Base %q Updated %q", ws.Base, ws.Updated)
	}
	if !exists(filepath.Join(ws.Dir, "theirs.txt")) || !exists(filepath.Join(ws.Dir, "mine.txt")) {
		t.Fatal("the merged tree is missing a side")
	}
	if n := gitIn(t, ws.Dir, "rev-list", "--count", "--merges", "HEAD"); n != "1" {
		t.Fatalf("merge commits: %s", n)
	}
	// What the branch changed since its base is still only its own work.
	if got := ws.FilesChanged(context.Background()); got != 1 {
		t.Fatalf("FilesChanged after the update: %d", got)
	}
	// A second call finds nothing to do and runs no git write.
	head := gitIn(t, ws.Dir, "rev-parse", "HEAD")
	res, err = ws.UpdateFromBase(context.Background(), config.WorktreeUpdateMerge)
	if err != nil || !res.UpToDate || gitIn(t, ws.Dir, "rev-parse", "HEAD") != head {
		t.Fatalf("second update: %+v %v", res, err)
	}
}

func TestUpdateFromBaseRebases(t *testing.T) {
	ws, repo := updateWorkspace(t)
	commitFile(t, repo, "theirs.txt", "theirs\n", "feat: theirs")
	if _, err := ws.UpdateFromBase(context.Background(), config.WorktreeUpdateRebase); err != nil {
		t.Fatal(err)
	}
	if n := gitIn(t, ws.Dir, "rev-list", "--count", "--merges", "HEAD"); n != "0" {
		t.Fatalf("a rebase made %s merge commits", n)
	}
	if parent := gitIn(t, ws.Dir, "rev-parse", "HEAD~1"); parent != gitIn(t, repo, "rev-parse", "main") {
		t.Fatal("the branch does not sit on the base's tip")
	}
}

func TestUpdateFromBaseConflictAbortsAndLeavesTheBranch(t *testing.T) {
	for _, mode := range []string{config.WorktreeUpdateMerge, config.WorktreeUpdateRebase} {
		t.Run(mode, func(t *testing.T) {
			ws, repo := updateWorkspace(t)
			commitFile(t, ws.Dir, "README", "mine\n", "feat: edit the readme")
			commitFile(t, repo, "README", "theirs\n", "feat: edit the readme there")
			head, base := gitIn(t, ws.Dir, "rev-parse", "HEAD"), ws.Base

			_, err := ws.UpdateFromBase(context.Background(), mode)
			var ce *ConflictError
			if !errors.As(err, &ce) || !errors.Is(err, ErrConflict) {
				t.Fatalf("want a conflict, got %v", err)
			}
			if len(ce.Paths) != 1 || ce.Paths[0] != "README" || ce.Mode != mode {
				t.Fatalf("%+v", ce)
			}
			if gitIn(t, ws.Dir, "rev-parse", "HEAD") != head || ws.Base != base || ws.Updated != "" {
				t.Fatal("a conflicting update moved the branch or the base")
			}
			if st := gitIn(t, ws.Dir, "status", "--porcelain", "--untracked-files=no"); st != "" {
				t.Fatalf("the abort left changes:\n%s", st)
			}
			if exists(filepath.Join(ws.Dir, ".git", "MERGE_HEAD")) {
				t.Fatal("a merge is still underway")
			}
		})
	}
}

func TestUpdateFromBaseRefusals(t *testing.T) {
	ctx := context.Background()
	ws, repo := updateWorkspace(t)
	commitFile(t, repo, "theirs.txt", "theirs\n", "feat: theirs")

	if _, err := ws.UpdateFromBase(ctx, config.WorktreeUpdateOff); !errors.Is(err, ErrUpdateOff) {
		t.Errorf("off: %v", err)
	}
	if _, err := ws.UpdateFromBase(ctx, "squash"); !errors.Is(err, ErrUpdateOff) {
		t.Errorf("an unknown mode: %v", err)
	}
	// A dirty tracked file stops it before any git write.
	os.WriteFile(filepath.Join(ws.Dir, "mine.txt"), []byte("edited\n"), 0o644)
	head := gitIn(t, ws.Dir, "rev-parse", "HEAD")
	if _, err := ws.UpdateFromBase(ctx, config.WorktreeUpdateMerge); !errors.Is(err, ErrDirty) {
		t.Errorf("dirty: %v", err)
	}
	if gitIn(t, ws.Dir, "rev-parse", "HEAD") != head {
		t.Error("a refused update moved HEAD")
	}
	gitIn(t, ws.Dir, "checkout", "--", "mine.txt")

	// A branch that is not the worktree's own is not updated.
	ws.Branch = "feature/x"
	if _, err := ws.UpdateFromBase(ctx, config.WorktreeUpdateMerge); err == nil || !strings.Contains(err.Error(), "not one of its own") {
		t.Errorf("foreign branch: %v", err)
	}
	ws.Branch = gitIn(t, ws.Dir, "symbolic-ref", "--short", "HEAD")

	// A worktree that left its branch is refused by the harness check.
	gitIn(t, ws.Dir, "checkout", "--detach", "-q")
	if _, err := ws.UpdateFromBase(ctx, config.WorktreeUpdateMerge); err == nil {
		t.Error("an update ran on a detached HEAD")
	}
	gitIn(t, ws.Dir, "checkout", "-q", ws.Branch)

	// No base branch, nothing to follow.
	ws.BaseRef = ""
	if _, err := ws.UpdateFromBase(ctx, config.WorktreeUpdateMerge); !errors.Is(err, ErrNoBaseRef) {
		t.Errorf("no base ref: %v", err)
	}
	// A ref git does not accept as a branch name is refused without being run.
	for _, bad := range []string{"--upload-pack=x", "a..b", "bad name", "-x"} {
		ws.BaseRef = bad
		if _, err := ws.UpdateFromBase(ctx, config.WorktreeUpdateMerge); err == nil {
			t.Errorf("base ref %q was accepted", bad)
		}
	}
	// A tampered .git file is refused.
	ws.BaseRef = "main"
	os.WriteFile(filepath.Join(ws.Dir, ".git"), []byte("gitdir: /tmp/evil\n"), 0o644)
	if _, err := ws.UpdateFromBase(ctx, config.WorktreeUpdateMerge); err == nil {
		t.Error("an update ran through a tampered .git file")
	}
}

func TestUpdateFromBaseOnASharedWorkspaceIsRefused(t *testing.T) {
	if _, err := SharedWorkspace(t.TempDir()).UpdateFromBase(context.Background(), config.WorktreeUpdateMerge); err == nil {
		t.Fatal("a shared workspace was updated")
	}
}

func TestBaseRefFollowsOnlyBranches(t *testing.T) {
	testEnv(t)
	repo := gitRepo(t)
	ctx := context.Background()
	sha := gitIn(t, repo, "rev-parse", "HEAD")
	ws, err := PrepareWorktree(ctx, repo, kanban.Item{ID: hookItem, Title: "t"}, sha)
	if err != nil {
		t.Fatal(err)
	}
	if ws.BaseRef != "" {
		t.Fatalf("a commit id base followed %q", ws.BaseRef)
	}
	if _, err := ws.UpdateFromBase(ctx, config.WorktreeUpdateMerge); !errors.Is(err, ErrNoBaseRef) {
		t.Fatalf("%v", err)
	}
}

func TestFetchBaseSkipsLocalBranches(t *testing.T) {
	ws, _ := updateWorkspace(t)
	if err := fetchBase(context.Background(), ws.repoRun, ws.repo, "main"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"--x/y", "a/--y", "/b", "a/"} {
		if err := fetchBase(context.Background(), ws.repoRun, ws.repo, bad); err != nil {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestConflictErrorWords(t *testing.T) {
	e := &ConflictError{Mode: "merge", Paths: []string{"a.go", "b.go"}, More: 3}
	got := e.Error()
	if !strings.Contains(got, "a.go, b.go and 3 more") || !strings.Contains(got, "nothing changed") {
		t.Fatalf("%q", got)
	}
}
