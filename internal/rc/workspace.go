package rc

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/forge"
	"github.com/vulnetix/belai/internal/gitinfo"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/trustgate"
)

// A start request may name a branch and a workspace: the session then runs in
// a linked worktree of the offered directory's repository under the host's
// worktrees directory (config.WorktreesDir), which the daemon reuses or makes.
// Both values are untrusted. The branch is held to the advertisement's shape
// and git's own check, the workspace to a short relative path of plain
// segments that stays under the worktrees directory, an existing workspace is
// used only when this repository lists it as its worktree, and every git call
// is hardened (forge.HardenedGit) and time-bounded.

// workspaceRE is the shape of a start request's Workspace: one to four plain
// segments, relative to the worktrees directory.
var workspaceRE = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+){0,3}$`)

// maxWorkspaceLen caps a Workspace.
const maxWorkspaceLen = 200

// workspaceTimeout bounds preparing one workspace, a fetch of the branch
// included.
const workspaceTimeout = 2 * time.Minute

// ValidWorkspace reports whether ws has the shape a start request's Workspace
// must have: plain segments, at most four, none of them "." or "..".
func ValidWorkspace(ws string) bool {
	if len(ws) > maxWorkspaceLen || !workspaceRE.MatchString(ws) {
		return false
	}
	for _, seg := range strings.Split(ws, "/") {
		if seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// validBranch reports whether b has the shape of a branch the website may
// name (the advertisement's shape, never an option or a "..").
func validBranch(b string) bool {
	return branchRE.MatchString(b) && !strings.Contains(b, "..") && !strings.HasPrefix(b, "-")
}

// Workspacer prepares the directory a start request runs in: cwd itself, or
// the worktree for branch and workspace. The error is the refusal reason.
type Workspacer func(ctx context.Context, cwd, branch, workspace string) (string, error)

// workspacePrep is prepareWorkspace with its git, worktrees directory and
// trust grant replaceable in tests.
type workspacePrep struct {
	run   forge.Runner
	root  func() (string, error)
	trust func(dir string) error
}

// prepareWorkspace is the daemon's Workspacer: the hardened git runner, the
// host's worktrees directory, and trust granted for the worktree it returns.
func prepareWorkspace(ctx context.Context, cwd, branch, workspace string) (string, error) {
	return workspacePrep{
		run:   forge.HardenedGit("", ""),
		root:  config.WorktreesDir,
		trust: func(dir string) error { return trustgate.Grant(dir, nil) },
	}.prepare(ctx, cwd, branch, workspace)
}

func (w workspacePrep) git(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, forge.WriteTimeout)
	defer cancel()
	out, err := w.run(ctx, dir, append([]string{"git"}, args...)...)
	return strings.TrimSpace(string(out)), err
}

// prepare returns the directory the session runs in. No branch and no
// workspace, or the branch cwd already has checked out and no workspace, is
// cwd. Otherwise it is <worktrees>/<workspace> (by default
// <owner>/<repo>/<branch slug>): an existing worktree of cwd's repository on
// the branch is reused, and a missing one is made, the branch fetched from
// origin first when the repository lacks it.
func (w workspacePrep) prepare(ctx context.Context, cwd, branch, workspace string) (string, error) {
	if branch == "" && workspace == "" {
		return cwd, nil
	}
	ctx, cancel := context.WithTimeout(ctx, workspaceTimeout)
	defer cancel()
	if branch != "" && !validBranch(branch) {
		return "", errors.New("that is not a branch name")
	}
	if workspace != "" && !ValidWorkspace(workspace) {
		return "", errors.New("that is not a workspace name: up to four plain path segments under the worktrees directory")
	}
	top, err := w.git(ctx, cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", errors.New("the directory is not a git checkout, so a session cannot start on a branch there")
	}
	if branch != "" {
		if err := forge.ValidBranchName(ctx, w.run, top, branch); err != nil {
			return "", err
		}
	}
	if workspace == "" {
		if current, _ := w.git(ctx, cwd, "branch", "--show-current"); current == branch {
			return cwd, nil
		}
		if workspace = defaultWorkspace(top, branch); !ValidWorkspace(workspace) {
			return "", errors.New("the branch name is too long for a workspace; name one")
		}
	}
	root, err := w.root()
	if err != nil {
		return "", fmt.Errorf("the worktrees directory: %v", err)
	}
	root = filepath.Clean(root)
	target := filepath.Join(root, filepath.FromSlash(workspace))
	if !within(root, target) {
		return "", errors.New("that workspace is outside the worktrees directory")
	}
	if _, err := os.Lstat(target); err == nil {
		return w.reuse(ctx, top, root, target, workspace, branch)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("workspace %s cannot be read", workspace)
	}
	if branch == "" {
		return "", fmt.Errorf("workspace %s does not exist; name a branch to make it", workspace)
	}
	if !forge.BranchExists(ctx, w.run, top, branch) {
		if err := w.fetchBranch(ctx, top, branch); err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return "", fmt.Errorf("create the worktrees directory: %v", err)
	}
	// A symlink on the way may not lead the worktree out of the directory.
	if !within(resolvePath(root), resolvePath(filepath.Dir(target))) {
		return "", errors.New("that workspace is outside the worktrees directory")
	}
	if err := forge.AddWorktree(ctx, w.run, top, target, branch, false); err != nil {
		return "", fmt.Errorf("git refused to make the worktree for %s: %s", branch, forge.CleanErr(err))
	}
	if err := w.trust(target); err != nil {
		return "", fmt.Errorf("trust the worktree: %v", err)
	}
	return target, nil
}

// reuse returns target, an existing path, when it is a worktree of top's
// repository on branch (any branch when branch is empty), and refuses it
// otherwise.
func (w workspacePrep) reuse(ctx context.Context, top, root, target, workspace, branch string) (string, error) {
	if !within(resolvePath(root), resolvePath(target)) {
		return "", errors.New("that workspace is outside the worktrees directory")
	}
	var found *forge.Worktree
	for _, wt := range w.worktrees(ctx, top) {
		if !wt.Bare && !wt.Prunable && resolvePath(wt.Path) == resolvePath(target) {
			found = &wt
			break
		}
	}
	if found == nil {
		return "", fmt.Errorf("workspace %s exists and is not a worktree of this repository", workspace)
	}
	// The worktree's own view of its repository must agree: its .git file is
	// what git follows in it.
	mine, err1 := w.commonDir(ctx, top)
	theirs, err2 := w.commonDir(ctx, target)
	if err1 != nil || err2 != nil || mine != theirs {
		return "", fmt.Errorf("workspace %s is not a worktree of this repository", workspace)
	}
	switch {
	case branch == "":
	case found.Branch == "":
		return "", fmt.Errorf("workspace %s has a detached HEAD, not branch %s", workspace, branch)
	case found.Branch != branch:
		return "", fmt.Errorf("workspace %s is on branch %s", workspace, cleanBranch(found.Branch))
	}
	if err := w.trust(target); err != nil {
		return "", fmt.Errorf("trust the worktree: %v", err)
	}
	return target, nil
}

// fetchBranch brings branch from origin and creates it tracking origin's, or
// says it is neither here nor there.
func (w workspacePrep) fetchBranch(ctx context.Context, top, branch string) error {
	if _, err := w.git(ctx, top, "remote", "get-url", "origin"); err != nil {
		return fmt.Errorf("there is no branch %s here and no origin to fetch it from", branch)
	}
	ref, remote := "refs/heads/"+branch, "refs/remotes/origin/"+branch
	if _, err := w.git(ctx, top, "fetch", "--no-tags", "--quiet", "origin", "--", "+"+ref+":"+remote); err != nil {
		return fmt.Errorf("branch %s is not here and could not be fetched from origin: %s", branch, forge.CleanErr(err))
	}
	if _, err := w.git(ctx, top, "branch", "--track", "--", branch, remote); err != nil {
		return fmt.Errorf("create branch %s: %s", branch, forge.CleanErr(err))
	}
	return nil
}

// worktrees lists top's repository's worktrees, or nil when git cannot.
func (w workspacePrep) worktrees(ctx context.Context, top string) []forge.Worktree {
	ctx, cancel := context.WithTimeout(ctx, forge.ReadTimeout)
	defer cancel()
	out, err := w.run(ctx, top, "git", "worktree", "list", "--porcelain")
	if err != nil {
		return nil
	}
	return forge.ParseWorktrees(string(out))
}

// commonDir is the resolved git common directory of the checkout at dir.
func (w workspacePrep) commonDir(ctx context.Context, dir string) (string, error) {
	c, err := w.git(ctx, dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(c) {
		c = filepath.Join(dir, c)
	}
	return resolvePath(c), nil
}

// defaultWorkspace is <owner>/<repo>/<branch slug> from the origin remote, or
// <checkout name>/<branch slug> when origin is not a forge address.
func defaultWorkspace(top, branch string) string {
	slug := strings.ReplaceAll(branch, "/", "-")
	if rem, ok := forge.ParseRemote(gitinfo.OriginURL(top)); ok && slugRE.MatchString(rem.Slug()) {
		if ws := rem.Slug() + "/" + slug; ValidWorkspace(ws) {
			return ws
		}
	}
	return plainSegment(filepath.Base(top)) + "/" + slug
}

// plainSegment folds s to the characters a workspace segment may hold.
func plainSegment(s string) string {
	b := []byte(s)
	for i, c := range b {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
		default:
			b[i] = '-'
		}
	}
	out := strings.Trim(string(b), "-.")
	if out == "" {
		return "repo"
	}
	return out
}

// cleanBranch is b when it has the advertised shape, else a placeholder: a
// refusal never echoes an odd ref name.
func cleanBranch(b string) string {
	if validBranch(b) {
		return b
	}
	return "another branch"
}

// within reports whether p is root or under it (both clean and absolute).
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// resolvePath cleans p and resolves its symlinks where it exists.
func resolvePath(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// dirWorktrees lists the linked worktrees of the repository at path that live
// under the worktrees directory, for the directory's advertisement: the path
// rebuilt from the directory and a checked workspace, and the branch when it
// has the advertised shape. At most sessionsync.RCMaxWorktrees; nil when path
// is not a checkout or git cannot say.
func dirWorktrees(ctx context.Context, path string) []sessionsync.RCWorktree {
	if _, ok := gitinfo.Detect(path); !ok {
		return nil
	}
	root, err := config.WorktreesDir()
	if err != nil {
		return nil
	}
	w := workspacePrep{run: forge.HardenedGit("", "")}
	return listWorktrees(w.worktrees(ctx, path), root, path)
}

// listWorktrees picks the advertised worktrees out of list.
func listWorktrees(list []forge.Worktree, root, path string) []sessionsync.RCWorktree {
	rroot, self := resolvePath(root), resolvePath(path)
	var out []sessionsync.RCWorktree
	for _, wt := range list {
		if len(out) >= sessionsync.RCMaxWorktrees {
			break
		}
		p := resolvePath(wt.Path)
		if wt.Bare || wt.Prunable || p == self || !within(rroot, p) {
			continue
		}
		rel, err := filepath.Rel(rroot, p)
		if err != nil {
			continue
		}
		ws := filepath.ToSlash(rel)
		if !ValidWorkspace(ws) {
			continue
		}
		e := sessionsync.RCWorktree{Path: filepath.Join(filepath.Clean(root), filepath.FromSlash(ws)), Workspace: ws}
		if validBranch(wt.Branch) {
			e.Branch = wt.Branch
		}
		out = append(out, e)
	}
	return out
}
