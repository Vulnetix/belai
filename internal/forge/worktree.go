package forge

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Worktree is one entry of `git worktree list --porcelain`.
type Worktree struct {
	Path     string
	Branch   string // short name; "" when detached or bare
	Head     string // short SHA
	Bare     bool
	Detached bool
	Locked   bool
	Prunable bool
	Dirty    bool // tracked changes present (not checked for bare trees)
	Current  bool // the worktree the session is in
}

// maxDirtyChecks bounds the per-worktree `git status` calls in one probe.
const maxDirtyChecks = 16

// ParseWorktrees parses `git worktree list --porcelain` output.
func ParseWorktrees(out string) []Worktree {
	var list []Worktree
	var cur *Worktree
	flush := func() {
		if cur != nil {
			list = append(list, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			flush()
			continue
		}
		key, val, _ := strings.Cut(line, " ")
		if key == "worktree" {
			flush()
			cur = &Worktree{Path: val}
			continue
		}
		if cur == nil {
			continue
		}
		switch key {
		case "HEAD":
			if len(val) > 7 {
				val = val[:7]
			}
			cur.Head = val
		case "branch":
			cur.Branch = strings.TrimPrefix(val, "refs/heads/")
		case "bare":
			cur.Bare = true
		case "detached":
			cur.Detached = true
		case "locked":
			cur.Locked = true
		case "prunable":
			cur.Prunable = true
		}
	}
	flush()
	return list
}

// ListWorktrees lists the repository's worktrees, marking the one whose path
// is root as current and checking each non-bare tree for tracked changes.
func ListWorktrees(ctx context.Context, r Runner, root string) ([]Worktree, error) {
	out, err := run(ctx, r, ReadTimeout, root, "git", "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	list := ParseWorktrees(out)
	checked := 0
	for i := range list {
		wt := &list[i]
		wt.Current = samePath(wt.Path, root)
		if wt.Bare || wt.Prunable || checked >= maxDirtyChecks {
			continue
		}
		checked++
		status, err := run(ctx, r, ReadTimeout, wt.Path, "git", "status", "--porcelain", "-uno")
		wt.Dirty = err == nil && status != ""
	}
	return list, nil
}

// AddWorktree creates a worktree at path. When newBranch is set the branch is
// created from HEAD; otherwise the existing branch is checked out there.
func AddWorktree(ctx context.Context, r Runner, root, path, branch string, newBranch bool) error {
	if err := argSafe(path, branch); err != nil {
		return err
	}
	argv := []string{"git", "worktree", "add"}
	if newBranch {
		argv = append(argv, "-b", branch, path)
	} else {
		argv = append(argv, path, branch)
	}
	_, err := run(ctx, r, WriteTimeout, root, argv...)
	return err
}

// Refusals for RemoveWorktree.
var (
	ErrRemoveCurrent = errors.New("refusing to remove the worktree the session is in")
	ErrRemoveMain    = errors.New("refusing to remove the main worktree")
	ErrNeedsForce    = errors.New("worktree is dirty or locked; removal needs force")
)

// RemoveWorktree removes wt. The current worktree and the main one (the first
// entry, which git itself will not remove) are always refused; a dirty or
// locked tree needs force.
func RemoveWorktree(ctx context.Context, r Runner, root string, wt Worktree, isMain, force bool) error {
	switch {
	case wt.Current:
		return ErrRemoveCurrent
	case isMain || wt.Bare:
		return ErrRemoveMain
	case (wt.Dirty || wt.Locked) && !force:
		return ErrNeedsForce
	}
	if err := argSafe(wt.Path); err != nil {
		return err
	}
	argv := []string{"git", "worktree", "remove"}
	if force {
		// A locked tree needs the flag twice.
		argv = append(argv, "--force")
		if wt.Locked {
			argv = append(argv, "--force")
		}
	}
	argv = append(argv, wt.Path)
	_, err := run(ctx, r, WriteTimeout, root, argv...)
	return err
}

// BranchExists reports whether refs/heads/branch exists.
func BranchExists(ctx context.Context, r Runner, root, branch string) bool {
	if argSafe(branch) != nil {
		return false
	}
	_, err := run(ctx, r, ReadTimeout, root, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// ValidBranchName asks git whether name is a valid branch name.
func ValidBranchName(ctx context.Context, r Runner, root, name string) error {
	if err := argSafe(name); err != nil {
		return err
	}
	if _, err := run(ctx, r, ReadTimeout, root, "git", "check-ref-format", "--branch", name); err != nil {
		return fmt.Errorf("invalid branch name %q", name)
	}
	return nil
}

// Push pushes branch to origin and sets its upstream.
func Push(ctx context.Context, r Runner, root, branch string) error {
	if err := argSafe(branch); err != nil {
		return err
	}
	_, err := run(ctx, r, WriteTimeout, root, "git", "push", "-u", "origin", branch)
	return err
}

// argSafe rejects empty values and ones git would read as an option.
func argSafe(vals ...string) error {
	for _, v := range vals {
		if strings.TrimSpace(v) == "" {
			return errors.New("empty argument")
		}
		if strings.HasPrefix(v, "-") {
			return fmt.Errorf("argument may not start with '-': %q", v)
		}
	}
	return nil
}

// samePath compares two paths after cleaning and resolving symlinks.
func samePath(a, b string) bool {
	return resolve(a) == resolve(b)
}

func resolve(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
