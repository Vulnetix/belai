package forge

import (
	"context"
	"os/exec"
)

// Snapshot is everything the git and ci tabs render, gathered in one probe.
// Every *Err field holds cleaned, display-ready text.
type Snapshot struct {
	Root        string // repository top level; "" when dir is not in a repo
	Branch      string // "" when detached
	Upstream    string // e.g. origin/feature; "" when none is set
	LastSubject string // subject of HEAD, the default PR title
	Remote      Remote
	Worktrees   []Worktree
	WorktreeErr string

	Provider Provider // nil when PR/CI actions are unavailable
	Reason   string   // why Provider is nil

	PR       *PR
	PRErr    string
	Checks   []Check
	CheckErr string
}

// Probe gathers a Snapshot for dir. It never fails outright: each missing
// piece leaves its field empty and, where useful, an error string behind.
// A nil look uses exec.LookPath.
func Probe(ctx context.Context, r Runner, look LookPath, dir string) Snapshot {
	if look == nil {
		look = exec.LookPath
	}
	var s Snapshot
	root, err := run(ctx, r, ReadTimeout, dir, "git", "rev-parse", "--show-toplevel")
	if err != nil || root == "" {
		s.Reason = "not a git repository"
		return s
	}
	s.Root = root
	s.Branch, _ = run(ctx, r, ReadTimeout, root, "git", "branch", "--show-current")
	s.Upstream, _ = run(ctx, r, ReadTimeout, root, "git", "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	subject, _ := run(ctx, r, ReadTimeout, root, "git", "log", "-1", "--format=%s")
	s.LastSubject = Clean(subject)

	if wts, err := ListWorktrees(ctx, r, root); err != nil {
		s.WorktreeErr = CleanErr(err)
	} else {
		s.Worktrees = wts
	}

	if raw, err := run(ctx, r, ReadTimeout, root, "git", "remote", "get-url", "origin"); err == nil {
		if rem, ok := ParseRemote(raw); ok {
			s.Remote = rem
		}
	}
	s.Provider, s.Reason = For(s.Remote, r, look)
	if s.Provider == nil {
		return s
	}
	if s.Branch == "" {
		s.PRErr = "detached HEAD — check out a branch for " + s.Provider.Noun() + " actions"
		return s
	}
	pr, err := s.Provider.PRForBranch(ctx, root, s.Branch)
	if err != nil {
		s.PRErr = CleanErr(err)
		return s
	}
	s.PR = pr
	if pr == nil {
		return s
	}
	checks, err := s.Provider.Checks(ctx, root, s.Branch, *pr)
	if err != nil {
		s.CheckErr = CleanErr(err)
	}
	s.Checks = checks
	return s
}

// CIAvailable reports whether the ci tab has something to show: a provider
// and a PR/MR for the branch.
func (s Snapshot) CIAvailable() bool {
	return s.Provider != nil && s.PR != nil
}

// Main reports whether wt is the repository's main worktree (git lists it
// first, and refuses to remove it).
func (s Snapshot) Main(wt Worktree) bool {
	return len(s.Worktrees) > 0 && s.Worktrees[0].Path == wt.Path
}
