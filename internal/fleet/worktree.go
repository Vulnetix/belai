package fleet

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/forge"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/proc"
	"github.com/vulnetix/belai/internal/sandbox"
)

// BranchPrefix starts every branch a worker creates or accepts from an item.
const BranchPrefix = "belai/"

// hardenedGit is every git call a worker makes: repository hooks and
// fsmonitor off (a repository's own config must not run code on an
// unattended worker's account), no file:// transport, no credential prompt,
// the scrubbed environment, its own process group, no stdin. With gitDir set
// it also pins --git-dir and --work-tree, so a worktree's .git file — which
// the worker's model can write — never decides which repository git opens.
func hardenedGit(gitDir, workTree string, extraEnv ...string) forge.Runner {
	return func(ctx context.Context, dir string, argv ...string) ([]byte, error) {
		if len(argv) == 0 {
			return nil, errors.New("fleet: empty command")
		}
		if argv[0] == "git" {
			pre := []string{"git",
				"-c", "core.hooksPath=" + os.DevNull,
				"-c", "core.fsmonitor=false",
				"-c", "protocol.file.allow=never",
				"-c", "credential.interactive=never",
			}
			if gitDir != "" {
				pre = append(pre, "--git-dir="+gitDir, "--work-tree="+workTree)
			}
			argv = append(pre, argv[1:]...)
		}
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Dir = dir
		cmd.Env = append(append(proc.ScrubbedEnv(), "GIT_TERMINAL_PROMPT=0"), extraEnv...)
		proc.SetProcessGroup(cmd)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			msg := strings.TrimSpace(stderr.String())
			if i := strings.LastIndexByte(msg, '\n'); i >= 0 {
				msg = msg[i+1:]
			}
			if ctx.Err() != nil {
				msg = "timed out"
			}
			if msg == "" {
				msg = err.Error()
			}
			return stdout.Bytes(), fmt.Errorf("%s: %s", argv[0], forge.Clean(msg))
		}
		return stdout.Bytes(), nil
	}
}

func git(ctx context.Context, r forge.Runner, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	out, err := r(ctx, dir, append([]string{"git"}, args...)...)
	return strings.TrimSpace(string(out)), err
}

// Workspace is where one item's work happens.
type Workspace struct {
	// Dir is the session's working directory and confinement root.
	Dir    string
	Branch string
	// Worktree is true when Dir is a worktree this worker created.
	Worktree bool
	// Base is the commit the item's work is measured from: the base a new
	// branch started at, or where an existing branch forked from HEAD.
	Base      string
	repo      string
	gitDir    string
	commonDir string
	dotgit    []byte
	run       forge.Runner // pinned to this worktree
	repoRun   forge.Runner // the main repository
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// identityEnv supplies a commit identity when the repository has none, so a
// worker's commit never fails on an unconfigured machine. The user's own
// identity, when set, is left alone.
func identityEnv(ctx context.Context, r forge.Runner, repo string) []string {
	if name, _ := git(ctx, r, repo, "config", "user.name"); name != "" {
		if email, _ := git(ctx, r, repo, "config", "user.email"); email != "" {
			return nil
		}
	}
	return []string{
		"GIT_AUTHOR_NAME=Belai agent", "GIT_AUTHOR_EMAIL=agent@belai.invalid",
		"GIT_COMMITTER_NAME=Belai agent", "GIT_COMMITTER_EMAIL=agent@belai.invalid",
	}
}

// PrepareWorktree creates a git worktree for item outside the repository.
// An item that already has a branch (a review, or a second attempt) checks
// that branch out; otherwise a new branch belai/K-xxxxxx/a<attempt> starts
// from base.
func PrepareWorktree(ctx context.Context, repo string, it kanban.Item, base string) (*Workspace, error) {
	root, err := config.WorktreesDir()
	if err != nil {
		return nil, err
	}
	repoRun := hardenedGit("", "")
	top, err := git(ctx, repoRun, repo, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("worktree: %s is not a git repository: %w", repo, err)
	}
	repo = top
	ident := identityEnv(ctx, repoRun, repo)
	repoRun = hardenedGit("", "", ident...)

	branch := it.Branch
	newBranch := branch == ""
	if !newBranch {
		if !strings.HasPrefix(branch, BranchPrefix) {
			return nil, fmt.Errorf("worktree: item branch %q is not a %s* branch", branch, BranchPrefix)
		}
		if err := forge.ValidBranchName(ctx, repoRun, repo, branch); err != nil {
			return nil, err
		}
		if !forge.BranchExists(ctx, repoRun, repo, branch) {
			// One of this item's own attempt branches that was deleted (a
			// failed attempt, or cleaned up after its pull request) starts
			// the item fresh; the release records the new branch. Any other
			// missing branch is refused.
			if !ownAttemptBranch(it, branch) {
				return nil, fmt.Errorf("worktree: branch %s does not exist in this repository", branch)
			}
			newBranch = true
		}
	}
	if newBranch {
		for n := it.Attempts + 1; ; n++ {
			branch = fmt.Sprintf("%s%s/a%d", BranchPrefix, it.Short(), n)
			if !forge.BranchExists(ctx, repoRun, repo, branch) {
				break
			}
			if n > it.Attempts+20 {
				return nil, errors.New("worktree: no free branch name for this item")
			}
		}
		if err := forge.ValidBranchName(ctx, repoRun, repo, branch); err != nil {
			return nil, err
		}
	}
	if base = strings.TrimSpace(base); base == "" {
		base = "HEAD"
	}
	if strings.HasPrefix(base, "-") {
		return nil, fmt.Errorf("worktree: invalid base %q", base)
	}
	// The commit the work is measured from, fixed now so the model cannot
	// move it.
	var baseSHA string
	if newBranch {
		baseSHA, err = git(ctx, repoRun, repo, "rev-parse", "--verify", "--end-of-options", base+"^{commit}")
	} else {
		baseSHA, err = git(ctx, repoRun, repo, "merge-base", "HEAD", branch)
	}
	if err != nil {
		return nil, fmt.Errorf("worktree: base: %w", err)
	}
	commonDir, err := git(ctx, repoRun, repo, "rev-parse", "--git-common-dir")
	if err != nil {
		return nil, fmt.Errorf("worktree: %w", err)
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(repo, commonDir)
	}
	commonDir = filepath.Clean(commonDir)

	_, projectKey := kanban.ProjectFor(repo)
	dir := filepath.Join(root, projectKey, it.Short()+"-"+randHex(3))
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return nil, err
	}
	args := []string{"worktree", "add"}
	if newBranch {
		args = append(args, "-b", branch, dir, base)
	} else {
		args = append(args, dir, branch)
	}
	if _, err := git(ctx, repoRun, repo, args...); err != nil {
		return nil, fmt.Errorf("worktree: %w", err)
	}
	// Read the worktree's .git pointer now, before any model has run in it,
	// and pin every later git call to the directory it names.
	dotgit, err := os.ReadFile(filepath.Join(dir, ".git"))
	if err != nil {
		return nil, fmt.Errorf("worktree: %w", err)
	}
	gitDir, ok := strings.CutPrefix(strings.TrimSpace(string(dotgit)), "gitdir: ")
	if !ok {
		return nil, errors.New("worktree: unexpected .git file")
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(dir, gitDir)
	}
	return &Workspace{
		Dir: dir, Branch: branch, Worktree: true, Base: baseSHA, repo: repo,
		gitDir: filepath.Clean(gitDir), commonDir: commonDir, dotgit: dotgit,
		run:     hardenedGit(gitDir, dir, ident...),
		repoRun: repoRun,
	}, nil
}

// ownAttemptBranch reports whether branch is one of the names PrepareWorktree
// mints for it: belai/K-xxxxxx/a<n>.
func ownAttemptBranch(it kanban.Item, branch string) bool {
	n, ok := strings.CutPrefix(branch, BranchPrefix+it.Short()+"/a")
	if !ok || n == "" || len(n) > 4 || n[0] == '0' {
		return false
	}
	return strings.Trim(n, "0123456789") == ""
}

// SharedWorkspace is the repository itself, for isolation: shared (and
// none): no branch, no commit.
func SharedWorkspace(repo string) *Workspace { return &Workspace{Dir: repo, repo: repo} }

// intact reports whether the worktree's .git pointer is still the one the
// harness read when it created the worktree.
func (w *Workspace) intact() error {
	if !w.Worktree {
		return nil
	}
	now, err := os.ReadFile(filepath.Join(w.Dir, ".git"))
	if err != nil || !bytes.Equal(now, w.dotgit) {
		return errors.New("the worktree's .git file was changed; refusing to run git in it")
	}
	return nil
}

// changedPaths lists the paths git status reports, renames by both names.
func (w *Workspace) changedPaths(ctx context.Context) ([]string, error) {
	out, err := w.run(ctx, w.Dir, "git", "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	var paths []string
	fields := strings.Split(string(out), "\x00")
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) < 4 {
			continue
		}
		status, path := f[:2], f[3:]
		paths = append(paths, path)
		if status[0] == 'R' || status[0] == 'C' {
			if i+1 < len(fields) && fields[i+1] != "" {
				paths = append(paths, fields[i+1])
			}
			i++
		}
	}
	return paths, nil
}

// Commit commits every change in the worktree to its branch and reports how
// many paths it committed (0 when there was nothing left to commit).
// The model may already have committed; this catches what it left.
func (w *Workspace) Commit(ctx context.Context, msg string) (int, error) {
	if !w.Worktree {
		return 0, nil
	}
	if err := w.intact(); err != nil {
		return 0, err
	}
	if err := w.onBranch(ctx); err != nil {
		return 0, err
	}
	paths, err := w.changedPaths(ctx)
	if err != nil {
		return 0, err
	}
	if len(paths) == 0 {
		return 0, nil
	}
	sha, err := forge.CommitPaths(ctx, w.run, w.Dir, paths, msg)
	if err != nil || sha == "" {
		return 0, err
	}
	return len(paths), nil
}

// onBranch reports whether the worktree is still on the item's branch. The
// model may commit on its branch; it must not leave it.
func (w *Workspace) onBranch(ctx context.Context) error {
	head, err := git(ctx, w.run, w.Dir, "symbolic-ref", "--quiet", "HEAD")
	if err != nil || head != "refs/heads/"+w.Branch {
		return fmt.Errorf("the worktree left its branch %s (HEAD is %q); the work was not committed", w.Branch, head)
	}
	return nil
}

// FilesChanged counts the files the branch changed since Base, the work
// both the model and the harness committed.
func (w *Workspace) FilesChanged(ctx context.Context) int {
	if !w.Worktree || w.Base == "" {
		return 0
	}
	out, err := git(ctx, w.run, w.Dir, "diff", "--name-only", w.Base+"..HEAD")
	if err != nil || out == "" {
		return 0
	}
	return len(strings.Split(out, "\n"))
}

// branchDir is the ref directory this item's commits may write: the item's
// own directory for a belai/K-xxxxxx/aN branch, so a model cannot touch
// another item's branches; the whole belai/ namespace for an older flat
// belai/K-xxxxxx-aN branch.
func (w *Workspace) branchDir() string {
	rel := strings.TrimSuffix(BranchPrefix, "/")
	if i := strings.LastIndexByte(w.Branch, '/'); i > len(BranchPrefix) {
		rel = w.Branch[:i]
	}
	return rel
}

// privateObjects is where the model's own git writes new objects: this
// worktree's admin dir, never the shared store.
func (w *Workspace) privateObjects() string { return filepath.Join(w.gitDir, "objects") }

// Sandbox is what the model's own git needs inside the OS sandbox: ordered
// mounts, and environment for its git.
//
// The mounts are layered:
//
//  1. the git common dir, writable, so git can create its lock files there
//     (git takes packed-refs.lock on every ref update);
//  2. every entry already in it, read-only — config, HEAD, index, hooks,
//     info, refs, logs, objects, the other worktrees, and any in-progress
//     state of the main checkout. A read-only mount cannot be written or
//     renamed over, so a model cannot plant a hook or config, move main,
//     or corrupt the object store. Anything it creates alongside them is
//     removed by Settle after the turn;
//  3. writable again, only this item's ref directory and its reflogs, and
//     this worktree's own admin dir (its HEAD, index and private objects).
//
// The environment points git at the private object store, with the shared
// store as a read-only alternate; Settle copies new objects across.
func (w *Workspace) Sandbox() (mounts []sandbox.Mount, env []string) {
	if !w.Worktree || w.commonDir == "" {
		return nil, nil
	}
	refs := filepath.Join(w.commonDir, "refs", "heads", filepath.FromSlash(w.branchDir()))
	logs := filepath.Join(w.commonDir, "logs", "refs", "heads", filepath.FromSlash(w.branchDir()))
	// A mount needs its target to exist.
	_ = os.MkdirAll(refs, 0o755)
	_ = os.MkdirAll(logs, 0o755)
	for _, d := range []string{"pack", "info"} {
		_ = os.MkdirAll(filepath.Join(w.privateObjects(), d), 0o755)
	}
	packed := filepath.Join(w.commonDir, "packed-refs")
	if _, err := os.Stat(packed); errors.Is(err, os.ErrNotExist) {
		// An empty packed-refs is valid; with it present and read-only, a
		// model cannot create one listing a ref of its choosing.
		_ = os.WriteFile(packed, nil, 0o644)
	}
	mounts = []sandbox.Mount{{Path: w.commonDir, Writable: true}}
	entries, _ := os.ReadDir(w.commonDir)
	for _, e := range entries {
		mounts = append(mounts, sandbox.Mount{Path: filepath.Join(w.commonDir, e.Name())})
	}
	mounts = append(mounts,
		sandbox.Mount{Path: refs, Writable: true},
		sandbox.Mount{Path: logs, Writable: true},
		sandbox.Mount{Path: w.gitDir, Writable: true},
	)
	env = []string{
		"GIT_OBJECT_DIRECTORY=" + w.privateObjects(),
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=" + filepath.Join(w.commonDir, "objects"),
	}
	return mounts, env
}

// RootEntries lists the git common dir's top level, for Settle.
func (w *Workspace) RootEntries() map[string]bool {
	out := map[string]bool{}
	if !w.Worktree || w.commonDir == "" {
		return out
	}
	entries, _ := os.ReadDir(w.commonDir)
	for _, e := range entries {
		out[e.Name()] = true
	}
	return out
}

// absorbObjects copies the objects the model's git wrote to its private store
// into the shared one, never overwriting an object there. It copies rather
// than hard-links: a link would share an inode the model can still write.
func (w *Workspace) absorbObjects() error {
	shared := filepath.Join(w.commonDir, "objects")
	priv := w.privateObjects()
	err := filepath.WalkDir(priv, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(priv, path)
		if err != nil || rel == filepath.Join("info", "alternates") || strings.HasSuffix(rel, ".lock") {
			return err
		}
		dst := filepath.Join(shared, rel)
		if _, err := os.Lstat(dst); err == nil {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		tmp := dst + ".belai-tmp"
		if err := os.WriteFile(tmp, data, 0o444); err != nil {
			return err
		}
		return os.Rename(tmp, dst)
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Settle runs after the model's turn, before the harness touches git: it
// copies the objects the model's git wrote into the shared store, and
// removes anything the model created at the top of the git common dir
// (before is RootEntries from before the turn) — a MERGE_HEAD or shallow
// file there would change the user's main checkout. It returns the names it
// removed.
func (w *Workspace) Settle(before map[string]bool) ([]string, error) {
	if !w.Worktree || w.commonDir == "" {
		return nil, nil
	}
	err := w.absorbObjects()
	var removed []string
	for name := range w.RootEntries() {
		if !before[name] {
			if rmErr := os.RemoveAll(filepath.Join(w.commonDir, name)); rmErr == nil {
				removed = append(removed, name)
			}
		}
	}
	sort.Strings(removed)
	return removed, err
}

// ForgeOrigin reports whether the repository's origin is a GitHub or GitLab
// remote a draft pull request can be opened on.
func (w *Workspace) ForgeOrigin(ctx context.Context) bool {
	if !w.Worktree {
		return false
	}
	origin, _ := git(ctx, w.repoRun, w.repo, "remote", "get-url", "origin")
	_, ok := forge.ParseRemote(origin)
	return ok
}

// PublishBranch pushes the item's branch — that branch only, to origin — and
// opens a draft pull request for it, or returns the one already open. It is
// the fleet's tools.Publisher: the model's PublishBranch tool and the
// harness's publish-at-done both come here. Only committed work is pushed.
func (w *Workspace) PublishBranch(ctx context.Context, title, body string) (string, error) {
	if !w.Worktree {
		return "", errors.New("publishing needs a worktree branch")
	}
	if err := w.intact(); err != nil {
		return "", err
	}
	if err := w.onBranch(ctx); err != nil {
		return "", err
	}
	// Check the remote before pushing: with no origin, git reads "origin" as
	// a local path, and the hardened runner refuses file transport with an
	// error that says nothing useful.
	origin, _ := git(ctx, w.repoRun, w.repo, "remote", "get-url", "origin")
	rem, ok := forge.ParseRemote(origin)
	if !ok {
		return "", errors.New("the repository has no GitHub or GitLab origin remote to publish to")
	}
	// The model's commits live in its private object store until settled.
	if err := w.absorbObjects(); err != nil {
		return "", err
	}
	if w.FilesChanged(ctx) == 0 {
		return "", fmt.Errorf("nothing is committed on %s yet: commit the work (git add, git commit) before publishing", w.Branch)
	}
	p, reason := forge.For(rem, w.run, exec.LookPath)
	if p == nil {
		return "", errors.New(reason)
	}
	// An explicit refspec: exactly this branch, to the same name.
	ref := "refs/heads/" + w.Branch
	if _, err := git(ctx, w.run, w.Dir, "push", "--set-upstream", "origin", ref+":"+ref); err != nil {
		return "", err
	}
	if pr, err := p.PRForBranch(ctx, w.Dir, w.Branch); err == nil && pr != nil && pr.URL != "" && pr.State != "closed" && pr.State != "merged" {
		return pr.URL, nil
	}
	return p.CreatePR(ctx, w.Dir, forge.CreatePRArgs{Branch: w.Branch, Title: title, Body: body, Draft: true})
}

// Remove deletes the worktree directory. The branch, and the work on it,
// stay in the repository.
func (w *Workspace) Remove(ctx context.Context) error {
	if !w.Worktree {
		return nil
	}
	if _, err := git(ctx, w.repoRun, w.repo, "worktree", "remove", "--force", w.Dir); err != nil {
		// A worktree whose .git was tampered with may not remove cleanly:
		// drop the directory and let git prune its record.
		_ = os.RemoveAll(w.Dir)
		_, _ = git(ctx, w.repoRun, w.repo, "worktree", "prune")
		return err
	}
	return nil
}
