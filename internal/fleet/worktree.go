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
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/forge"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/sandbox"
	"github.com/vulnetix/belai/internal/vaultenv"
)

// BranchPrefix starts every branch a worker creates or accepts from an item.
const BranchPrefix = "belai/"

// hardenedGit is every git call a worker makes (forge.HardenedGit): hooks and
// fsmonitor off, no file:// transport, no credential prompt, the scrubbed
// environment and its own process group.
func hardenedGit(gitDir, workTree string, extraEnv ...string) forge.Runner {
	return forge.HardenedGit(gitDir, workTree, extraEnv...)
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
	// Owned is the profile's owned directory (config.ProfileFilesDir), the fallback
	// source for a workspace.sync path the repository does not hold yet: a library
	// install writes the files a profile carries there, never into a repository.
	Owned string
	// Base is the commit the item's work is measured from: the base a new
	// branch started at, or where an existing branch forked from HEAD.
	Base string
	repo string
	// synced are the repository-relative paths a profile's workspace.sync
	// copied in. The harness's commit never includes them (see changedPaths).
	synced []string
	// placed are the files the harness itself put in the worktree (reference
	// documents and synced files), by worktree-relative path. They are never
	// committed.
	placed    map[string]bool
	gitDir    string
	commonDir string
	dotgit    []byte
	run       forge.Runner // pinned to this worktree
	repoRun   forge.Runner // the main repository
	// ident is the commit identity env the worktree's runner was built with, so a
	// runner for publishing can be built the same way with a credential added.
	ident []string
	// published is the commit the last successful PublishBranch pushed.
	published string
	// kept is the pull request kept when this branch's own was closed as its
	// duplicate: the item continues on kept.Branch.
	kept forge.PR
	// coord, when set, lets a push that fails for an infrastructure reason be
	// handed to the forge coordinator (escalate.go); pending is the last
	// request filed and filed every request id this workspace filed.
	coord   *Coordinator
	pending *pendingRequest
	filedMu sync.Mutex
	filed   map[string]bool
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
		if !forge.BranchExists(ctx, repoRun, repo, branch) && ownAttemptBranch(it, branch) {
			// The machine that worked it may be gone with its disk (a sandbox
			// stop and start), having pushed the branch before it went: resume
			// from origin rather than start over.
			fetchOwnBranch(ctx, repo, ident, branch)
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
		ident:   ident,
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
		if w.isSynced(path) {
			// A crew's shared scratchpad is never part of the branch.
			continue
		}
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

// MaxHarnessCommitPaths caps what the harness commits on a worker's behalf.
const MaxHarnessCommitPaths = 1000

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
	// A change no agent writes by hand: a module or build cache, a vendored tree
	// or an install directory left in the worktree. Committing it would bury the
	// work in a pull request nobody can review, so the attempt fails instead.
	if len(paths) > MaxHarnessCommitPaths {
		return 0, fmt.Errorf("the worktree holds %d uncommitted paths, more than %d, which looks like a cache or build output left in the repository; it was not committed", len(paths), MaxHarnessCommitPaths)
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
	if _, err := w.originRemote(ctx); err != nil {
		return "", err
	}
	// The model's commits live in its private object store until settled.
	if err := w.absorbObjects(); err != nil {
		return "", err
	}
	if w.FilesChanged(ctx) == 0 {
		return "", w.nothingToPublish(ctx)
	}
	// A failure that is the machine's or the forge's (a refused or missing
	// credential, a rate limit, a server error, no gh) hands the branch to the
	// forge coordinator when one is set; a failure that is the branch's never
	// does (escalate.go).
	p, run, credArgs, err := w.forgeAuthed(ctx)
	if err != nil {
		return "", w.handOff(ctx, ForgeKindPullRequest, err)
	}
	// An explicit refspec: exactly this branch, to the same name.
	ref := "refs/heads/" + w.Branch
	pushArgs := append(slices.Clone(credArgs), "push", "--set-upstream", "origin", ref+":"+ref)
	if _, err := git(ctx, run, w.Dir, pushArgs...); err != nil {
		return "", w.handOff(ctx, ForgeKindPullRequest, err)
	}
	head, _ := git(ctx, w.run, w.Dir, "rev-parse", "HEAD")
	if pr, err := p.PRForBranch(ctx, w.Dir, w.Branch); err == nil && pr != nil && pr.URL != "" && pr.State != "closed" && pr.State != "merged" {
		w.published = strings.TrimSpace(head)
		return pr.URL, nil
	}
	url, err := p.CreatePR(ctx, w.Dir, forge.CreatePRArgs{Branch: w.Branch, Title: title, Body: body, Draft: true})
	if err != nil {
		return "", w.handOff(ctx, ForgeKindPullRequest, err)
	}
	w.published = strings.TrimSpace(head)
	return url, nil
}

// forgeAuthed returns the forge provider for the repository's origin, the
// runner and git arguments that carry the forge credential. Check the remote
// first: with no origin, git reads "origin" as a local path, and the hardened
// runner refuses file transport with an error that says nothing useful.
// Publishing is the one step that needs a forge credential. The scrubbed
// environment a worker's git runs in has none, so a GitHub token the vault holds
// for this machine (held in memory, never shown to the model) is handed to the
// push and to gh, and to nothing else.
func (w *Workspace) forgeAuthed(ctx context.Context) (forge.Provider, forge.Runner, []string, error) {
	rem, err := w.originRemote(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	run, credArgs := w.run, []string(nil)
	if env, helper := publishAuth(rem, time.Now()); len(env) > 0 {
		run = hardenedGit(w.gitDir, w.Dir, append(slices.Clone(w.ident), env...)...)
		credArgs = helper
	}
	p, reason := forge.For(rem, run, exec.LookPath)
	if p == nil {
		return nil, nil, nil, errors.New(reason)
	}
	return p, run, credArgs, nil
}

// fetchOwnBranch fetches one of the item's own attempt branches from origin into
// the local branch of the same name, with the forge credential the vault grants
// this machine. It is best effort: when the branch is not on origin, or origin
// cannot be reached, the item starts fresh as before.
func fetchOwnBranch(ctx context.Context, repo string, ident []string, branch string) {
	repoRun := hardenedGit("", "", ident...)
	origin, _ := git(ctx, repoRun, repo, "remote", "get-url", "origin")
	rem, ok := forge.ParseRemote(origin)
	if !ok {
		return
	}
	run, credArgs := repoRun, []string(nil)
	if env, helper := publishAuth(rem, time.Now()); len(env) > 0 {
		run = hardenedGit("", "", append(slices.Clone(ident), env...)...)
		credArgs = helper
	}
	fctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	ref := "refs/heads/" + branch
	_, _ = git(fctx, run, repo, append(credArgs, "fetch", "--no-tags", "origin", ref+":"+ref)...)
}

// PushBranch pushes the worktree's branch to origin without opening a pull
// request: the work in progress of a worker that is being stopped, so a machine
// that goes with its disk loses none of it.
func (w *Workspace) PushBranch(ctx context.Context) error {
	if !w.Worktree {
		return errors.New("pushing needs a worktree branch")
	}
	if err := w.intact(); err != nil {
		return err
	}
	rem, err := w.originRemote(ctx)
	if err != nil {
		return err
	}
	if err := w.absorbObjects(); err != nil {
		return err
	}
	if w.FilesChanged(ctx) == 0 {
		return nil
	}
	run, credArgs := w.run, []string(nil)
	if env, helper := publishAuth(rem, time.Now()); len(env) > 0 {
		run = hardenedGit(w.gitDir, w.Dir, append(slices.Clone(w.ident), env...)...)
		credArgs = helper
	}
	ref := "refs/heads/" + w.Branch
	if _, err := git(ctx, run, w.Dir, append(slices.Clone(credArgs), "push", "--set-upstream", "origin", ref+":"+ref)...); err != nil {
		return w.handOff(ctx, ForgeKindPush, err)
	}
	return nil
}

// originRemote is the repository's GitHub or GitLab origin.
func (w *Workspace) originRemote(ctx context.Context) (forge.Remote, error) {
	origin, _ := git(ctx, w.repoRun, w.repo, "remote", "get-url", "origin")
	rem, ok := forge.ParseRemote(origin)
	if !ok {
		return rem, errors.New("the repository has no GitHub or GitLab origin remote to publish to")
	}
	return rem, nil
}

// cardBranchPrefix is the part of an item branch that every attempt shares:
// belai/K-3588f2/ for belai/K-3588f2/a3.
func cardBranchPrefix(branch string) string {
	if i := strings.LastIndex(branch, "/"); i > 0 {
		return branch[:i+1]
	}
	return ""
}

// CardPRs lists the open pull requests from every attempt branch of this
// item: a retry pushes a new branch, so an earlier attempt's pull request can
// still be open beside this one.
func (w *Workspace) CardPRs(ctx context.Context) ([]forge.PR, error) {
	if !w.Worktree {
		return nil, nil
	}
	prefix := cardBranchPrefix(w.Branch)
	if prefix == "" {
		return nil, nil
	}
	p, _, _, err := w.forgeAuthed(ctx)
	if err != nil {
		return nil, err
	}
	return p.OpenPRsWithPrefix(ctx, w.Dir, prefix)
}

// CloseDuplicatePR closes the item's open pull request number as a duplicate
// of keep, another of the item's open pull requests, with reason as the
// comment. Nothing outside the item's own pull requests can be closed.
func (w *Workspace) CloseDuplicatePR(ctx context.Context, number, keep int, reason string) (closed, kept forge.PR, err error) {
	if number == keep {
		return closed, kept, errors.New("a pull request cannot be a duplicate of itself")
	}
	prs, err := w.CardPRs(ctx)
	if err != nil {
		return closed, kept, err
	}
	var okClose, okKeep bool
	for _, pr := range prs {
		switch pr.Number {
		case number:
			closed, okClose = pr, true
		case keep:
			kept, okKeep = pr, true
		}
	}
	if !okClose || !okKeep {
		return closed, kept, fmt.Errorf("both pull requests must be open ones from this item's branches (%s*)", cardBranchPrefix(w.Branch))
	}
	p, _, _, err := w.forgeAuthed(ctx)
	if err != nil {
		return closed, kept, err
	}
	comment := fmt.Sprintf("Closed as a duplicate of #%d. %s", keep, reason)
	if err := p.ClosePR(ctx, w.Dir, number, strings.TrimSpace(comment)); err != nil {
		return closed, kept, err
	}
	if closed.Branch == w.Branch {
		w.kept = kept
	}
	return closed, kept, nil
}

// Kept is the pull request kept over this branch's own, when that one was
// closed as its duplicate; the zero PR otherwise.
func (w *Workspace) Kept() forge.PR {
	if w == nil {
		return forge.PR{}
	}
	return w.kept
}

// PublishedCurrent reports whether the branch is exactly what the last
// PublishBranch pushed: the same commit, and nothing uncommitted beside it.
func (w *Workspace) PublishedCurrent(ctx context.Context) bool {
	if w == nil || !w.Worktree {
		return false
	}
	head, err := git(ctx, w.run, w.Dir, "rev-parse", "HEAD")
	if err != nil {
		return false
	}
	head = strings.TrimSpace(head)
	// Published by this turn, or already on origin: a retry that resumed a
	// branch whose pull request is open has nothing new to push, and a worker
	// re-checking that work has not failed it.
	if head != w.published && head != w.originHead(ctx) {
		return false
	}
	paths, err := w.changedPaths(ctx)
	return err == nil && len(paths) == 0
}

// originHead is the commit origin's copy of the worktree's branch points at,
// read with the forge credential the vault grants this machine; "" when the
// branch is not on origin or origin cannot be read.
func (w *Workspace) originHead(ctx context.Context) string {
	rem, err := w.originRemote(ctx)
	if err != nil {
		return ""
	}
	run, credArgs := w.run, []string(nil)
	if env, helper := publishAuth(rem, time.Now()); len(env) > 0 {
		run = hardenedGit(w.gitDir, w.Dir, append(slices.Clone(w.ident), env...)...)
		credArgs = helper
	}
	lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := git(lctx, run, w.Dir, append(slices.Clone(credArgs), "ls-remote", "origin", "refs/heads/"+w.Branch)...)
	if err != nil {
		return ""
	}
	if f := strings.Fields(out); len(f) > 0 && len(f[0]) == 40 {
		return f[0]
	}
	return ""
}

// publishAuth returns the environment and git arguments that let a push and the
// GitHub CLI act with the GITHUB_TOKEN (or GH_TOKEN) the secrets vault granted
// this machine: the token for the child process only, and a credential helper
// for that one host that reads it through gh. It returns nothing for a remote
// that is not on GitHub, or when the vault holds no such token, and then the
// push behaves as it always did.
func publishAuth(rem forge.Remote, now time.Time) (env, gitArgs []string) {
	if rem.Kind != forge.KindGitHub || rem.Host == "" {
		return nil, nil
	}
	token := ""
	for _, kv := range vaultenv.Default.Environ(now) {
		if name, val, ok := strings.Cut(kv, "="); ok && val != "" && (name == "GH_TOKEN" || (name == "GITHUB_TOKEN" && token == "")) {
			token = val
		}
	}
	if token == "" {
		return nil, nil
	}

	return []string{"GH_TOKEN=" + token, "GITHUB_TOKEN=" + token},
		[]string{"-c", "credential.https://" + rem.Host + ".helper=", "-c", "credential.https://" + rem.Host + ".helper=!gh auth git-credential"}
}

// nothingToPublish explains an empty branch. Uncommitted edits only need a
// commit; a clean worktree means the base already has everything, which a
// model has mistaken for its own earlier commit, so the error names the base
// and says what to do instead of sending it back to git commit.
func (w *Workspace) nothingToPublish(ctx context.Context) error {
	if paths, err := w.changedPaths(ctx); err == nil && len(paths) > 0 {
		return fmt.Errorf("nothing is committed on %s yet, and %d path(s) are changed in the worktree: commit them (git add, git commit) before publishing", w.Branch, len(paths))
	}
	base := w.Base
	if len(base) > 12 {
		base = base[:12]
	}
	return fmt.Errorf("%s has no changes beyond its base %s, so there is nothing to publish. "+
		"A commit that is an ancestor of %s (git merge-base --is-ancestor <commit> %s) is already on the base, not work on this branch. "+
		"If the base already does what the item asks, say so in the item's notes and finish without publishing", w.Branch, base, base, base)
}

// unchanged reports whether a worker-made belai/ branch still sits at its
// base: no commit of its own, so nothing is lost by deleting it.
func (w *Workspace) unchanged(ctx context.Context) bool {
	if !w.Worktree || w.Base == "" || !strings.HasPrefix(w.Branch, BranchPrefix) {
		return false
	}
	head, err := git(ctx, w.run, w.Dir, "rev-parse", "HEAD")
	if err != nil {
		return false
	}
	base, err := git(ctx, w.run, w.Dir, "rev-parse", w.Base+"^{commit}")
	return err == nil && head != "" && head == base
}

// Discard removes the worktree, and its branch too when the branch holds no
// commit of its own: a read-only worker (a scout running tests) or a turn
// that changed nothing would otherwise leave one empty branch per item. A
// branch with work on it stays in the repository.
func (w *Workspace) Discard(ctx context.Context) error {
	empty := w.unchanged(ctx)
	err := w.Remove(ctx)
	if empty && err == nil {
		_, _ = git(ctx, w.repoRun, w.repo, "branch", "-D", w.Branch)
	}
	return err
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

// Head returns the commit the worktree stands at: the tip of the item's
// branch once its work is committed.
func (w *Workspace) Head(ctx context.Context) (string, error) {
	if err := w.intact(); err != nil {
		return "", err
	}
	out, err := git(ctx, w.run, w.Dir, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", err
	}
	if ref := kanban.CleanRef(out); ref != "" {
		return ref, nil
	}
	return "", errors.New("the worktree's HEAD is not a commit id")
}
