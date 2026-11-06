package fleet

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/attribution"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/forge"
	"github.com/vulnetix/belai/internal/mergefix"
)

// Update from base (docs/worktrees.md). The harness brings a worktree's branch
// up to date with the ref it was made from, by merge or rebase, between turns.
// The model never names the ref, the mode or an argument: the ref is the one
// recorded when the worktree was made, checked by git as a branch name, and the
// mode is the user's worktrees.update_mode.

// Refusals from UpdateFromBase. Each is a harness sentence.
var (
	// ErrUpdateOff means worktrees.update_mode is off, or the mode is not one
	// this build knows.
	ErrUpdateOff = errors.New("updating from the base is off (worktrees.update_mode)")
	// ErrNoBaseRef means the worktree has no branch to follow: it was made from a
	// commit id or a pull request head.
	ErrNoBaseRef = errors.New("this worktree was not made from a branch, so there is nothing to update from")
	// ErrDirty means the worktree has uncommitted changes to tracked files.
	ErrDirty = errors.New("the worktree has uncommitted changes; commit or discard them first")
	// ErrConflict is wrapped by a *ConflictError.
	ErrConflict = errors.New("the update conflicts with the base")
)

// ConflictError reports an update that conflicted. It was aborted: the branch
// and the worktree are as they were.
type ConflictError struct {
	// Mode is merge or rebase.
	Mode string
	// Paths are the conflicting files, as cleaned note names, at most
	// mergefix.MaxNoteFiles.
	Paths []string
	// More counts the conflicting files beyond Paths.
	More int
}

// Error is a harness sentence naming the mode and the files.
func (e *ConflictError) Error() string {
	s := fmt.Sprintf("the %s conflicts in %s", e.Mode, strings.Join(e.Paths, ", "))
	if e.More > 0 {
		s += fmt.Sprintf(" and %d more", e.More)
	}
	return s + "; it was aborted and nothing changed"
}

// Unwrap lets errors.Is match ErrConflict.
func (e *ConflictError) Unwrap() error { return ErrConflict }

// UpdateResult is what an update did.
type UpdateResult struct {
	Mode string
	// Ref is the branch followed and Commit the commit of it that was brought in.
	Ref    string
	Commit string
	// Brought counts the base's commits the branch did not have.
	Brought int
	// UpToDate is true when the branch already held the base: nothing was run.
	UpToDate bool
}

// String is a harness sentence.
func (r UpdateResult) String() string {
	if r.UpToDate {
		return fmt.Sprintf("already up to date with %s", r.Ref)
	}
	return fmt.Sprintf("brought in %d commit(s) of %s by %s", r.Brought, r.Ref, r.Mode)
}

// fetchBase refreshes a remote-tracking base before an update. A test replaces
// it, because the hardened git refuses a local origin. Failure is not an error:
// the update then uses what the repository already holds.
var fetchBase = func(ctx context.Context, run forge.Runner, repo, ref string) error {
	remote, branch, ok := strings.Cut(ref, "/")
	if !ok || remote == "" || branch == "" || strings.HasPrefix(remote, "-") || strings.HasPrefix(branch, "-") {
		return nil
	}
	if _, err := git(ctx, run, repo, "show-ref", "--verify", "--quiet", "refs/remotes/"+ref); err != nil {
		return nil
	}
	fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err := git(fctx, run, repo, "fetch", "--no-tags", "--quiet", "--end-of-options", remote, branch)
	return err
}

// UpdateFromBase brings the worktree's branch up to date with BaseRef by mode
// (merge or rebase). It refuses unless the worktree is the one the harness
// made, on its own belai/ branch, with no uncommitted tracked changes. A
// conflict aborts the merge or rebase and returns a *ConflictError; the branch
// is then as it was. Base moves only after the update succeeded.
func (w *Workspace) UpdateFromBase(ctx context.Context, mode string) (UpdateResult, error) {
	res := UpdateResult{Mode: mode}
	if mode != config.WorktreeUpdateMerge && mode != config.WorktreeUpdateRebase {
		return res, ErrUpdateOff
	}
	if !w.Worktree {
		return res, errors.New("only a worktree the harness made can be updated")
	}
	if w.BaseRef == "" {
		return res, ErrNoBaseRef
	}
	if !strings.HasPrefix(w.Branch, BranchPrefix) {
		return res, fmt.Errorf("the worktree is on %q, not one of its own %s branches", w.Branch, BranchPrefix)
	}
	if err := w.intact(); err != nil {
		return res, err
	}
	if err := w.onBranch(ctx); err != nil {
		return res, err
	}
	if err := forge.ValidBranchName(ctx, w.repoRun, w.repo, w.BaseRef); err != nil {
		return res, fmt.Errorf("the base %q is not a branch name git accepts", w.BaseRef)
	}
	res.Ref = w.BaseRef
	if dirty, err := git(ctx, w.run, w.Dir, "status", "--porcelain", "--untracked-files=no"); err != nil {
		return res, fmt.Errorf("could not read the worktree's status: %s", forge.CleanErr(err))
	} else if dirty != "" {
		return res, ErrDirty
	}
	_ = fetchBase(ctx, w.repoRun, w.repo, w.BaseRef)
	tip, err := git(ctx, w.run, w.Dir, "rev-parse", "--verify", "--end-of-options", w.BaseRef+"^{commit}")
	if err != nil || !commitRE.MatchString(tip) {
		return res, fmt.Errorf("the base %s does not name a commit here any more", w.BaseRef)
	}
	res.Commit = tip
	before, err := git(ctx, w.run, w.Dir, "rev-parse", "--verify", "HEAD")
	if err != nil || !commitRE.MatchString(before) {
		return res, errors.New("could not read the worktree's HEAD")
	}
	if _, err := git(ctx, w.run, w.Dir, "merge-base", "--is-ancestor", tip, before); err == nil {
		res.UpToDate = true
		w.markUpdated(tip, mode)
		return res, nil
	}
	if n, err := git(ctx, w.run, w.Dir, "rev-list", "--count", before+".."+tip); err == nil {
		res.Brought, _ = strconv.Atoi(n)
	}

	// An editor or a pager must never open: the commands run with none.
	run := hardenedGit(w.gitDir, w.Dir, append(append([]string(nil), w.ident...),
		"GIT_EDITOR=true", "GIT_SEQUENCE_EDITOR=true", "GIT_MERGE_AUTOEDIT=no")...)
	var opErr error
	switch mode {
	case config.WorktreeUpdateMerge:
		msg := attribution.Message("merge "+tip[:12]+" of "+w.BaseRef+" into "+w.Branch, w.Session)
		_, opErr = git(ctx, run, w.Dir, "merge", "--no-edit", "--no-ff", "-m", msg, "--end-of-options", tip)
	default:
		_, opErr = git(ctx, run, w.Dir, "rebase", "--end-of-options", tip)
	}
	if opErr != nil {
		return res, w.abortUpdate(ctx, run, mode, before, opErr)
	}
	after, err := git(ctx, w.run, w.Dir, "rev-parse", "--verify", "HEAD")
	if err != nil || !commitRE.MatchString(after) {
		return res, errors.New("the update finished but the worktree's HEAD could not be read")
	}
	if mode == config.WorktreeUpdateMerge {
		w.noteCommit(ctx)
	}
	w.markUpdated(tip, mode)
	return res, nil
}

// markUpdated moves the commit the work is measured from to the base tip the
// branch now holds, and tells the record.
func (w *Workspace) markUpdated(tip, mode string) {
	w.Base, w.Updated, w.UpdatedBy = tip, tip, mode
	w.updateRecordBase()
}

// abortUpdate undoes a failed merge or rebase, checks the branch is where it
// was, and reports the failure: a *ConflictError when files conflicted.
func (w *Workspace) abortUpdate(ctx context.Context, run forge.Runner, mode, before string, opErr error) error {
	var paths []string
	if out, err := git(ctx, w.run, w.Dir, "diff", "--name-only", "--diff-filter=U", "-z"); err == nil {
		for _, p := range strings.Split(out, "\x00") {
			if p = strings.TrimSpace(p); p != "" {
				paths = append(paths, p)
			}
		}
	}
	abort := "--abort"
	if mode == config.WorktreeUpdateMerge {
		_, _ = git(ctx, run, w.Dir, "merge", abort)
	} else {
		_, _ = git(ctx, run, w.Dir, "rebase", abort)
	}
	now, err := git(ctx, w.run, w.Dir, "rev-parse", "--verify", "HEAD")
	if err != nil || now != before {
		return fmt.Errorf("the %s failed and the branch could not be put back to %s; inspect the worktree by hand", mode, before[:12])
	}
	if len(paths) == 0 {
		return fmt.Errorf("the %s failed: %s", mode, forge.CleanErr(opErr))
	}
	ce := &ConflictError{Mode: mode}
	for i, p := range paths {
		if i >= mergefix.MaxNoteFiles {
			ce.More = len(paths) - i
			break
		}
		ce.Paths = append(ce.Paths, mergefix.NoteFile(p))
	}
	return ce
}
