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
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/forge"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/proc"
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
	repo     string
	gitDir   string
	dotgit   []byte
	run      forge.Runner // pinned to this worktree
	repoRun  forge.Runner // the main repository
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
// that branch out; otherwise a new branch belai/K-xxxxxx-a<attempt> starts
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
			return nil, fmt.Errorf("worktree: branch %s does not exist in this repository", branch)
		}
	} else {
		for n := it.Attempts + 1; ; n++ {
			branch = fmt.Sprintf("%s%s-a%d", BranchPrefix, it.Short(), n)
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
		Dir: dir, Branch: branch, Worktree: true, repo: repo,
		gitDir: gitDir, dotgit: dotgit,
		run:     hardenedGit(gitDir, dir, ident...),
		repoRun: repoRun,
	}, nil
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
// many paths it committed. The harness commits; the model is told not to,
// and the repository's .git stays read-only inside the sandbox.
func (w *Workspace) Commit(ctx context.Context, msg string) (int, error) {
	if !w.Worktree {
		return 0, nil
	}
	if err := w.intact(); err != nil {
		return 0, err
	}
	paths, err := w.changedPaths(ctx)
	if err != nil || len(paths) == 0 {
		return 0, err
	}
	sha, err := forge.CommitPaths(ctx, w.run, w.Dir, paths, msg)
	if err != nil {
		return 0, err
	}
	if sha == "" {
		return 0, nil
	}
	return len(paths), nil
}

// Publish pushes the branch and opens a draft pull request, returning its
// URL. The title and body are the caller's harness-composed text.
func (w *Workspace) Publish(ctx context.Context, title, body string) (string, error) {
	if !w.Worktree {
		return "", errors.New("publish needs a worktree branch")
	}
	if err := w.intact(); err != nil {
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
	if err := forge.Push(ctx, w.run, w.Dir, w.Branch); err != nil {
		return "", err
	}
	p, reason := forge.For(rem, w.run, exec.LookPath)
	if p == nil {
		return "", errors.New(reason)
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
