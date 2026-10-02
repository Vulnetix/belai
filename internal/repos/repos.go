// Package repos keeps the repositories of the `repos` setting cloned on this
// machine (docs/library-items.md#repositories). It is what `belai repo sync` and
// `belai repo status` run.
//
// Everything goes through git as an argv, never a shell. A repository is cloned
// under the repos directory (config.ReposDir) and moved only forward: a branch is
// fast-forwarded and never reset, a tag or a commit is a detached checkout, and a
// checkout with local changes to tracked files, or a branch that has diverged
// from origin, is refused with the reason and left exactly as it was. Untracked
// files are never touched. Git runs with hooks off, no prompts, the scrubbed
// environment and only the https and ssh transports.
//
// No credential is ever stored or put in a URL. A public repository is fetched
// with every credential helper switched off. A private repository over https is
// fetched through the GitHub CLI's credential helper (`gh auth git-credential`)
// when `gh` is installed and signed in to that host, and the sync fails with that
// reason when it is not; a private repository over ssh uses the ssh agent and
// keys, in batch mode.
package repos

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/libitem"
	"github.com/vulnetix/belai/internal/proc"
	"github.com/vulnetix/belai/internal/sanitize"
)

// DefaultTimeout bounds one git call.
const DefaultTimeout = 10 * time.Minute

// ErrDisabled is returned for a repository whose enabled is false.
var ErrDisabled = errors.New("the repository is disabled")

// Runner runs one command in dir with env and returns its standard output. A
// failure carries the command's own message (its standard error, cleaned).
type Runner func(ctx context.Context, dir string, env []string, argv ...string) ([]byte, error)

// Options adjust where and how git runs; the zero value is production.
type Options struct {
	// ReposDir is where clones live. Empty means config.ReposDir().
	ReposDir string
	// Run runs a command. nil runs it with os/exec.
	Run Runner
	// Env is appended to the environment of every call, last, so it wins. It is for
	// tests that point a URL at a local repository.
	Env []string
	// LookPath finds a program (exec.LookPath unless replaced).
	LookPath func(string) (string, error)
	// Getenv reads the host environment (os.Getenv unless replaced).
	Getenv func(string) string
	// Timeout bounds one call (DefaultTimeout when zero).
	Timeout time.Duration
}

// Result says what a sync did.
type Result struct {
	Name string
	// Path is the checkout.
	Path string
	// Action is "cloned" (a new checkout), "updated" (the checkout moved) or
	// "current" (nothing to do).
	Action string
	// Ref is the first ref, the one that is checked out, as kind:name.
	Ref string
	// Commit is the checked out commit, abbreviated.
	Commit string
	// Notes are things worth saying: local commits ahead of origin, refs fetched.
	Notes []string
}

type engine struct {
	o       Options
	repo    config.GitRepo
	dir     string
	git     string
	credArg []string
	env     []string
}

func (o Options) withDefaults() Options {
	if o.Run == nil {
		o.Run = execRunner
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.ReposDir == "" {
		o.ReposDir, _ = config.ReposDir()
	}
	return o
}

// execRunner is the production Runner.
func execRunner(ctx context.Context, dir string, env []string, argv ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	proc.SetProcessGroup(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.Bytes(), &gitError{msg: sanitize.Clip(msg, 400), err: err}
	}
	return stdout.Bytes(), nil
}

type gitError struct {
	msg string
	err error
}

func (e *gitError) Error() string { return e.msg }
func (e *gitError) Unwrap() error { return e.err }

// ResolveDir is the checkout directory of a repository under base: the repos
// directory joined with its dir. It refuses a path that leaves base and any
// symbolic link along the way, so a checkout never lands somewhere a link points.
func ResolveDir(base string, r config.GitRepo) (string, error) {
	if base == "" {
		return "", errors.New("no repos directory")
	}
	rel := r.Subdir()
	if err := libitem.ValidateRepo(r); err != nil {
		return "", err
	}
	dir := filepath.Join(base, filepath.FromSlash(rel))
	if !strings.HasPrefix(dir, filepath.Clean(base)+string(filepath.Separator)) {
		return "", fmt.Errorf("%q leaves the repos directory", rel)
	}
	// Every existing component below base must be a real directory.
	cur := filepath.Clean(base)
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		cur = filepath.Join(cur, seg)
		fi, err := os.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			return "", err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%s is a symbolic link; a repository is never cloned through one", cur)
		}
	}
	return dir, nil
}

func (e *engine) run(ctx context.Context, dir string, args ...string) (string, error) {
	argv := append([]string{e.git,
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.fsmonitor=false",
		"-c", "advice.detachedHead=false",
		"-c", "credential.helper=",
	}, e.credArg...)
	argv = append(argv, args...)
	cctx, cancel := context.WithTimeout(ctx, e.o.Timeout)
	defer cancel()
	out, err := e.o.Run(cctx, dir, e.env, argv...)
	return strings.TrimSpace(string(out)), err
}

// gitIn runs git in the checkout.
func (e *engine) gitIn(ctx context.Context, args ...string) (string, error) {
	return e.run(ctx, e.dir, args...)
}

func newEngine(r config.GitRepo, o Options) (*engine, error) {
	o = o.withDefaults()
	if err := libitem.ValidateRepo(r); err != nil {
		return nil, fmt.Errorf("repository %s is not valid: %w", r.Name, err)
	}
	gitPath, err := o.LookPath("git")
	if err != nil {
		return nil, errors.New("git is not installed or not on PATH")
	}
	dir, err := ResolveDir(o.ReposDir, r)
	if err != nil {
		return nil, err
	}
	e := &engine{o: o, repo: r, dir: dir, git: gitPath}
	e.env = append(proc.ScrubbedEnv(),
		"GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GIT_ALLOW_PROTOCOL=https:ssh", "LC_ALL=C")
	if o.Getenv("GIT_SSH_COMMAND") == "" {
		e.env = append(e.env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	}
	return e, nil
}

// credentials sets up how a private repository authenticates, or fails with the
// reason it cannot. A public repository gets none.
func (e *engine) credentials(ctx context.Context) error {
	if e.repo.Visibility != "private" {
		return nil
	}
	if strings.HasPrefix(e.repo.URL, "git@") {
		return nil // ssh: the agent and the user's keys, in batch mode
	}
	u, err := url.Parse(e.repo.URL)
	if err != nil {
		return err
	}
	gh, err := e.o.LookPath("gh")
	if err != nil {
		return fmt.Errorf("%s is private and is fetched over https through the GitHub CLI, which is not installed (install gh and run `gh auth login`, or use an ssh url)", e.repo.Name)
	}
	// The scrubbed environment drops the token variables gh reads; hand back only
	// the ones it is documented to use, to gh and to the credential helper.
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_HOST", "GH_CONFIG_DIR"} {
		if v := e.o.Getenv(k); v != "" {
			e.env = append(e.env, k+"="+v)
		}
	}
	e.env = append(e.env, e.o.Env...)
	if _, err := e.o.Run(ctx, "", e.env, gh, "auth", "status", "--hostname", u.Hostname()); err != nil {
		return fmt.Errorf("%s is private and the GitHub CLI is not signed in to %s: run `gh auth login --hostname %s`", e.repo.Name, u.Hostname(), u.Hostname())
	}
	e.credArg = []string{"-c", "credential.https://" + u.Host + ".helper=!gh auth git-credential"}
	return nil
}

// Sync clones the repository if it is missing and brings its first ref up to date,
// fetching the rest. It never resets, never removes a tracked change and never
// touches an untracked file.
func Sync(ctx context.Context, r config.GitRepo, o Options) (Result, error) {
	if !r.IsEnabled() {
		return Result{}, ErrDisabled
	}
	e, err := newEngine(r, o)
	if err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(filepath.Dir(e.dir), 0o700); err != nil {
		return Result{}, err
	}
	unlock, err := config.AcquireFileLock(e.dir + ".lock")
	if err != nil {
		return Result{}, fmt.Errorf("another sync of %s is running", r.Name)
	}
	defer unlock()
	if err := e.credentials(ctx); err != nil {
		return Result{}, err
	}
	e.env = append(e.env, e.o.Env...)

	res := Result{Name: r.Name, Path: e.dir, Ref: r.Refs[0].Kind + ":" + r.Refs[0].Name}
	fresh, created, err := e.prepare(ctx)
	if err != nil {
		return res, err
	}
	before, _ := e.gitIn(ctx, "rev-parse", "--verify", "-q", "HEAD")
	if err := e.fetch(ctx, &res); err != nil {
		if created {
			_ = os.RemoveAll(e.dir)
		}
		return res, err
	}
	if err := e.checkout(ctx, r.Refs[0], &res); err != nil {
		if created {
			_ = os.RemoveAll(e.dir)
		}
		return res, err
	}
	if r.Submodules != nil && *r.Submodules {
		args := []string{"-c", "protocol.file.allow=never", "submodule", "update", "--init", "--recursive", "-q"}
		if d := e.depth(); d > 0 {
			args = append(args, "--depth", strconv.Itoa(d))
		}
		if _, err := e.gitIn(ctx, args...); err != nil {
			return res, fmt.Errorf("submodules: %w", err)
		}
	}
	after, _ := e.gitIn(ctx, "rev-parse", "--verify", "-q", "HEAD")
	short, _ := e.gitIn(ctx, "rev-parse", "--short=12", "HEAD")
	res.Commit = short
	switch {
	case fresh:
		res.Action = "cloned"
	case before != after:
		res.Action = "updated"
	default:
		res.Action = "current"
	}
	return res, nil
}

func (e *engine) depth() int {
	if e.repo.Depth != nil {
		return *e.repo.Depth
	}
	return 0
}

// prepare makes the directory a git repository whose origin is the url, or
// verifies it is one. fresh is true when it held no history yet; created is true
// when this call made the directory.
func (e *engine) prepare(ctx context.Context) (fresh, created bool, err error) {
	fi, statErr := os.Lstat(e.dir)
	switch {
	case errors.Is(statErr, os.ErrNotExist):
		if err := os.MkdirAll(e.dir, 0o700); err != nil {
			return false, false, err
		}
		created = true
	case statErr != nil:
		return false, false, statErr
	case !fi.IsDir():
		return false, false, fmt.Errorf("%s exists and is not a directory", e.dir)
	}
	if _, err := os.Lstat(filepath.Join(e.dir, ".git")); errors.Is(err, os.ErrNotExist) {
		des, _ := os.ReadDir(e.dir)
		if len(des) > 0 {
			return false, false, fmt.Errorf("%s exists and is not a git checkout; move it away or choose another dir", e.dir)
		}
		if _, err := e.gitIn(ctx, "init", "-q"); err != nil {
			return false, created, err
		}
		if _, err := e.gitIn(ctx, "remote", "add", "origin", e.repo.URL); err != nil {
			if created {
				_ = os.RemoveAll(e.dir)
			}
			return false, created, err
		}
		return true, created, nil
	}
	// The configured url, as written: `remote get-url` would show it after any
	// url.<base>.insteadOf rewrite, which is not what the settings name.
	origin, err := e.gitIn(ctx, "config", "--get", "remote.origin.url")
	if err != nil || origin != e.repo.URL {
		return false, false, fmt.Errorf("the clone at %s has origin %q, not %q; it is left alone (fix the url or remove the directory)", e.dir, clip(origin), e.repo.URL)
	}
	_, headErr := e.gitIn(ctx, "rev-parse", "--verify", "-q", "HEAD")
	return headErr != nil, false, nil
}

func clip(s string) string { return sanitize.Clip(s, 120) }

// fetch brings every listed ref into the clone. A branch lands in its
// remote-tracking ref (forced: that ref is only a mirror), a tag is fetched as it
// is and never moved, and a commit is fetched by id when it is not already there.
func (e *engine) fetch(ctx context.Context, res *Result) error {
	var specs, shas []string
	for _, ref := range e.repo.Refs {
		switch ref.Kind {
		case config.GitRefBranch:
			specs = append(specs, "+refs/heads/"+ref.Name+":refs/remotes/origin/"+ref.Name)
		case config.GitRefTag:
			specs = append(specs, "refs/tags/"+ref.Name+":refs/tags/"+ref.Name)
		case config.GitRefSHA:
			shas = append(shas, ref.Name)
		}
	}
	base := []string{"fetch", "-q", "--no-tags", "--no-recurse-submodules"}
	if d := e.depth(); d > 0 {
		base = append(base, "--depth", strconv.Itoa(d))
	}
	if len(specs) > 0 {
		if _, err := e.gitIn(ctx, append(append(append([]string{}, base...), "origin"), specs...)...); err != nil {
			return fmt.Errorf("fetch: %w", err)
		}
	}
	for _, sha := range shas {
		if e.has(ctx, sha) {
			continue
		}
		_, _ = e.gitIn(ctx, append(append([]string{}, base...), "origin", sha)...)
		if !e.has(ctx, sha) && e.depth() == 0 {
			// A server that will not serve a commit by id still has it on a branch.
			_, _ = e.gitIn(ctx, "fetch", "-q", "--no-tags", "--no-recurse-submodules", "origin", "+refs/heads/*:refs/remotes/origin/*")
		}
		if !e.has(ctx, sha) {
			return fmt.Errorf("commit %s was not found on origin", sha)
		}
	}
	if len(e.repo.Refs) > 1 {
		res.Notes = append(res.Notes, fmt.Sprintf("fetched %d other ref%s", len(e.repo.Refs)-1, map[bool]string{true: "", false: "s"}[len(e.repo.Refs) == 2]))
	}
	return nil
}

func (e *engine) has(ctx context.Context, sha string) bool {
	_, err := e.gitIn(ctx, "cat-file", "-e", sha+"^{commit}")
	return err == nil
}

func (e *engine) rev(ctx context.Context, name string) (string, error) {
	return e.gitIn(ctx, "rev-parse", "--verify", "-q", name)
}

// dirty reports whether a tracked file has a change, staged or not.
func (e *engine) dirty(ctx context.Context) (bool, error) {
	out, err := e.gitIn(ctx, "status", "--porcelain", "--untracked-files=no")
	return out != "", err
}

func (e *engine) isAncestor(ctx context.Context, a, b string) bool {
	_, err := e.gitIn(ctx, "merge-base", "--is-ancestor", a, b)
	return err == nil
}

// currentBranch is the branch HEAD is on, or "" when it is detached or unborn.
func (e *engine) currentBranch(ctx context.Context) string {
	out, err := e.gitIn(ctx, "symbolic-ref", "-q", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return out
}

// checkout puts the clone on ref.
func (e *engine) checkout(ctx context.Context, ref config.GitRef, res *Result) error {
	head, headErr := e.rev(ctx, "HEAD")
	unborn := headErr != nil
	switch ref.Kind {
	case config.GitRefBranch:
		tip, err := e.rev(ctx, "refs/remotes/origin/"+ref.Name)
		if err != nil {
			return fmt.Errorf("branch %s was not found on origin", ref.Name)
		}
		if unborn {
			if _, err := e.gitIn(ctx, "checkout", "-q", "-B", ref.Name, "refs/remotes/origin/"+ref.Name, "--"); err != nil {
				return err
			}
			_, _ = e.gitIn(ctx, "branch", "-q", "--set-upstream-to=origin/"+ref.Name, ref.Name)
			return nil
		}
		cur := e.currentBranch(ctx)
		if cur != ref.Name || head != tip {
			if d, err := e.dirty(ctx); err != nil {
				return err
			} else if d {
				return fmt.Errorf("%s has local changes to tracked files, so it was not moved to branch %s (commit or stash them)", e.repo.Name, ref.Name)
			}
		}
		if cur != ref.Name {
			if _, err := e.rev(ctx, "refs/heads/"+ref.Name); err == nil {
				if _, err := e.gitIn(ctx, "checkout", "-q", ref.Name, "--"); err != nil {
					return err
				}
			} else {
				if _, err := e.gitIn(ctx, "checkout", "-q", "-B", ref.Name, "refs/remotes/origin/"+ref.Name, "--"); err != nil {
					return err
				}
				_, _ = e.gitIn(ctx, "branch", "-q", "--set-upstream-to=origin/"+ref.Name, ref.Name)
			}
			head, _ = e.rev(ctx, "HEAD")
		}
		switch {
		case head == tip:
		case e.isAncestor(ctx, head, tip):
			if _, err := e.gitIn(ctx, "merge", "-q", "--ff-only", "refs/remotes/origin/"+ref.Name); err != nil {
				return fmt.Errorf("branch %s could not be fast-forwarded: %w", ref.Name, err)
			}
		case e.isAncestor(ctx, tip, head):
			res.Notes = append(res.Notes, fmt.Sprintf("branch %s has local commits ahead of origin; left as they are", ref.Name))
		default:
			return fmt.Errorf("branch %s has diverged from origin, so it was not changed (rebase or merge it yourself)", ref.Name)
		}
		return nil
	case config.GitRefTag, config.GitRefSHA:
		name := ref.Name
		if ref.Kind == config.GitRefTag {
			name = "refs/tags/" + ref.Name
		}
		target, err := e.rev(ctx, name+"^{commit}")
		if err != nil {
			return fmt.Errorf("%s %s was not found on origin", ref.Kind, ref.Name)
		}
		if !unborn && head == target && e.currentBranch(ctx) == "" {
			return nil
		}
		if !unborn {
			if d, err := e.dirty(ctx); err != nil {
				return err
			} else if d {
				return fmt.Errorf("%s has local changes to tracked files, so it was not moved to %s %s (commit or stash them)", e.repo.Name, ref.Kind, ref.Name)
			}
		}
		_, err = e.gitIn(ctx, "checkout", "-q", "--detach", target, "--")
		return err
	}
	return fmt.Errorf("unknown ref kind %q", ref.Kind)
}

// Status is what `belai repo status` shows for one repository. It reads the
// clone and its remote-tracking refs as the last sync left them; it fetches
// nothing.
type Status struct {
	Name string
	Path string
	// State is one of: disabled, not cloned, current, behind, ahead, diverged,
	// other ref, not fetched, invalid.
	State string
	// Detail says more: how far behind or ahead, what it is on instead.
	Detail string
	Commit string
	// Dirty is true when a tracked file has a change.
	Dirty bool
}

// StatusOf reports where a repository stands.
func StatusOf(ctx context.Context, r config.GitRepo, o Options) Status {
	st := Status{Name: r.Name}
	e, err := newEngine(r, o)
	if err != nil {
		st.State, st.Detail = "invalid", err.Error()
		return st
	}
	e.env = append(e.env, e.o.Env...)
	st.Path = e.dir
	if !r.IsEnabled() {
		st.State = "disabled"
		return st
	}
	if _, err := os.Lstat(filepath.Join(e.dir, ".git")); err != nil {
		st.State = "not cloned"
		return st
	}
	head, err := e.rev(ctx, "HEAD")
	if err != nil {
		st.State, st.Detail = "not fetched", "the clone has no commits yet; run belai repo sync"
		return st
	}
	st.Commit, _ = e.gitIn(ctx, "rev-parse", "--short=12", "HEAD")
	st.Dirty, _ = e.dirty(ctx)
	ref := r.Refs[0]
	switch ref.Kind {
	case config.GitRefBranch:
		if cur := e.currentBranch(ctx); cur != ref.Name {
			st.State = "other ref"
			if cur == "" {
				cur = "a detached commit"
			}
			st.Detail = "on " + cur + ", not branch " + ref.Name
			return st
		}
		tip, err := e.rev(ctx, "refs/remotes/origin/"+ref.Name)
		if err != nil {
			st.State, st.Detail = "not fetched", "branch "+ref.Name+" has not been fetched; run belai repo sync"
			return st
		}
		counts, err := e.gitIn(ctx, "rev-list", "--left-right", "--count", "HEAD...refs/remotes/origin/"+ref.Name)
		fields := strings.Fields(counts)
		if err != nil || len(fields) != 2 {
			st.State, st.Detail = "invalid", "could not compare with origin"
			return st
		}
		ahead, behind := fields[0], fields[1]
		switch {
		case head == tip:
			st.State = "current"
		case ahead == "0":
			st.State, st.Detail = "behind", behind+" commit"+plural(behind)+" behind origin as last fetched"
		case behind == "0":
			st.State, st.Detail = "ahead", ahead+" local commit"+plural(ahead)+" not on origin"
		default:
			st.State, st.Detail = "diverged", ahead+" ahead, "+behind+" behind origin as last fetched"
		}
	default:
		name := ref.Name
		if ref.Kind == config.GitRefTag {
			name = "refs/tags/" + ref.Name
		}
		target, err := e.rev(ctx, name+"^{commit}")
		switch {
		case err != nil:
			st.State, st.Detail = "not fetched", ref.Kind+" "+ref.Name+" has not been fetched; run belai repo sync"
		case target == head && e.currentBranch(ctx) == "":
			st.State = "current"
		default:
			st.State, st.Detail = "other ref", "not on "+ref.Kind+" "+ref.Name
		}
	}
	return st
}

func plural(n string) string {
	if n == "1" {
		return ""
	}
	return "s"
}
