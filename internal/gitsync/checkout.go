package gitsync

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/vulnetix/belai/internal/forge"
)

// branchShape is the branch a web request may name: the characters the rc
// advertisement keeps (rc dirgit.go), never an option and never "..". Git's
// own check-ref-format runs after it.
var branchShape = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)

// CanCheckout says whether Checkout would switch to branch now, without
// changing anything: the branch has a usable name and the repository is quiet
// (no uncommitted tracked changes, no rebase, merge, cherry-pick, revert or
// bisect underway). It does not look for the branch, which may need a fetch.
func (h *Hygiene) CanCheckout(ctx context.Context, branch string) error {
	_, err := h.checkoutReady(ctx, branch)
	return err
}

// checkoutReady runs CanCheckout's checks and returns the branch checked out
// now ("" when HEAD is detached).
func (h *Hygiene) checkoutReady(ctx context.Context, branch string) (string, error) {
	if !branchShape.MatchString(branch) || strings.HasPrefix(branch, "-") || strings.Contains(branch, "..") {
		return "", errors.New("that is not a branch name")
	}
	if _, err := h.git(ctx, forge.ReadTimeout, "check-ref-format", "--branch", branch); err != nil {
		return "", fmt.Errorf("%q is not a valid branch name", branch)
	}
	gitDir, err := h.git(ctx, forge.ReadTimeout, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", errors.New("the session is not in a git repository")
	}
	if op := inProgress(gitDir); op != "" {
		return "", errors.New(op + " is in progress")
	}
	dirty, err := h.git(ctx, forge.ReadTimeout, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return "", errors.New(forge.CleanErr(err))
	}
	if dirty != "" {
		return "", errors.New("the checkout has uncommitted changes; commit or stash them first")
	}
	current, _ := h.git(ctx, forge.ReadTimeout, "branch", "--show-current")
	return current, nil
}

// Checkout switches the session's checkout to branch and returns the line the
// transcript shows. It refuses what CanCheckout refuses, never stashes or
// discards anything, and a branch the repository lacks is fetched from origin
// and created tracking origin/<branch>. Git's own refusal (the branch is
// checked out in another worktree, a file would be overwritten) is the error.
// A detached HEAD may be switched away from. The next turn's sync (when it is
// on) sees the new HEAD and syncs the branch as it would after a commit.
func (h *Hygiene) Checkout(ctx context.Context, branch string) (string, error) {
	current, err := h.checkoutReady(ctx, branch)
	if err != nil {
		return "", err
	}
	if current == branch {
		return "already on " + branch, nil
	}
	fetched := false
	if _, err := h.git(ctx, forge.ReadTimeout, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
		if _, err := h.git(ctx, forge.ReadTimeout, "remote", "get-url", "origin"); err != nil {
			return "", fmt.Errorf("there is no branch %s here and no origin to fetch it from", branch)
		}
		ref := "refs/heads/" + branch
		if _, err := h.git(ctx, forge.WriteTimeout, "fetch", "--no-tags", "--quiet", "origin", "--", "+"+ref+":refs/remotes/origin/"+branch); err != nil {
			return "", fmt.Errorf("branch %s is not here and could not be fetched from origin: %s", branch, forge.CleanErr(err))
		}
		if _, err := h.git(ctx, forge.ReadTimeout, "branch", "--track", "--", branch, "refs/remotes/origin/"+branch); err != nil {
			return "", fmt.Errorf("create branch %s: %s", branch, forge.CleanErr(err))
		}
		fetched = true
	}
	// The trailing "--" makes git read branch as a revision, never a path.
	if _, err := h.git(ctx, forge.WriteTimeout, "checkout", "--quiet", branch, "--"); err != nil {
		return "", fmt.Errorf("git refused to check out %s: %s", branch, forge.CleanErr(err))
	}
	h.mu.Lock()
	h.info = nil
	h.reported = ""
	h.mu.Unlock()
	h.Kick()
	if fetched {
		return "checked out " + branch + " (fetched from origin)", nil
	}
	return "checked out " + branch, nil
}
