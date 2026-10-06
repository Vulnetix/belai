package repos

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/belai/internal/config"
)

const testURL = "https://example.test/acme/app.git"

// origin is a local bare repository standing in for the remote. The tests reach
// it through a url rewrite, so the repository config carries a real https url and
// the engine never sees a path.
type origin struct {
	t    *testing.T
	bare string
	work string
	home string
}

func gitEnv(home string) []string {
	return append(os.Environ(),
		"HOME="+home, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test",
		"GIT_ALLOW_PROTOCOL=file:https:ssh", "LC_ALL=C")
}

func newOrigin(t *testing.T) *origin {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	o := &origin{t: t, bare: filepath.Join(root, "origin.git"), work: filepath.Join(root, "work"), home: filepath.Join(root, "home")}
	for _, d := range []string{o.home, o.work} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	o.git(root, "init", "-q", "--bare", "-b", "main", o.bare)
	o.git(o.work, "init", "-q", "-b", "main")
	o.git(o.work, "remote", "add", "origin", o.bare)
	o.commit("a.txt", "one\n", "first")
	o.git(o.work, "push", "-q", "origin", "main")
	return o
}

func (o *origin) git(dir string, args ...string) string {
	o.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv(o.home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		o.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit writes a file in the work tree and commits it.
func (o *origin) commit(name, body, msg string) string {
	o.t.Helper()
	if err := os.WriteFile(filepath.Join(o.work, name), []byte(body), 0o644); err != nil {
		o.t.Fatal(err)
	}
	o.git(o.work, "add", name)
	o.git(o.work, "commit", "-q", "-m", msg)
	return o.git(o.work, "rev-parse", "HEAD")
}

func (o *origin) push(args ...string) {
	o.t.Helper()
	o.git(o.work, append([]string{"push", "-q", "origin"}, args...)...)
}

// opts points the engine at the repos directory and the url at the origin.
func (o *origin) opts(base string) Options {
	return Options{
		ReposDir: base,
		Env: []string{
			"HOME=" + o.home, "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
			"GIT_ALLOW_PROTOCOL=file:https:ssh",
			"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=url." + o.bare + ".insteadOf", "GIT_CONFIG_VALUE_0=" + testURL,
		},
	}
}

func repo(refs ...config.GitRef) config.GitRepo {
	if len(refs) == 0 {
		refs = []config.GitRef{{Kind: "branch", Name: "main"}}
	}
	return config.GitRepo{Name: "app", URL: testURL, Visibility: "public", Auth: "none", Refs: refs}
}

func branch(n string) config.GitRef { return config.GitRef{Kind: "branch", Name: n} }
func tag(n string) config.GitRef    { return config.GitRef{Kind: "tag", Name: n} }
func sha(n string) config.GitRef    { return config.GitRef{Kind: "sha", Name: n} }

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func headOf(o *origin, dir string) string { return o.git(dir, "rev-parse", "HEAD") }

func TestSyncClonesThenFastForwards(t *testing.T) {
	o := newOrigin(t)
	base := filepath.Join(t.TempDir(), "repos")
	r := repo()
	res, err := Sync(context.Background(), r, o.opts(base))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "app")
	if res.Action != "cloned" || res.Path != dir || res.Ref != "branch:main" || len(res.Commit) != 12 {
		t.Fatalf("result = %+v", res)
	}
	if read(t, filepath.Join(dir, "a.txt")) != "one\n" {
		t.Fatal("the file is not checked out")
	}
	if got := o.git(dir, "symbolic-ref", "--short", "HEAD"); got != "main" {
		t.Fatalf("on %q", got)
	}
	if fi, _ := os.Stat(dir); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o700 {
		t.Errorf("clone directory mode = %v", fi.Mode().Perm())
	}
	if again, err := Sync(context.Background(), r, o.opts(base)); err != nil || again.Action != "current" {
		t.Fatalf("second sync: %+v %v", again, err)
	}
	// Origin moves: the next sync fast-forwards.
	o.commit("b.txt", "two\n", "second")
	o.push("main")
	up, err := Sync(context.Background(), r, o.opts(base))
	if err != nil || up.Action != "updated" {
		t.Fatalf("update: %+v %v", up, err)
	}
	if read(t, filepath.Join(dir, "b.txt")) != "two\n" {
		t.Fatal("the new file did not arrive")
	}
	// No lock file is left behind.
	if _, err := os.Stat(dir + ".lock"); !os.IsNotExist(err) {
		t.Errorf("lock left: %v", err)
	}
}

func TestSyncNeverResetsADivergedBranch(t *testing.T) {
	o := newOrigin(t)
	base := t.TempDir()
	r := repo()
	if _, err := Sync(context.Background(), r, o.opts(base)); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "app")
	// A local commit ahead of origin is left as it is.
	if err := os.WriteFile(filepath.Join(dir, "local.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o.git(dir, "add", "local.txt")
	o.git(dir, "commit", "-q", "-m", "local")
	mine := headOf(o, dir)
	res, err := Sync(context.Background(), r, o.opts(base))
	if err != nil || res.Action != "current" || len(res.Notes) == 0 || !strings.Contains(strings.Join(res.Notes, " "), "ahead of origin") {
		t.Fatalf("ahead: %+v %v", res, err)
	}
	// Now origin moves too: diverged, refused, nothing changed.
	o.commit("c.txt", "theirs\n", "theirs")
	o.push("main")
	_, err = Sync(context.Background(), r, o.opts(base))
	if err == nil || !strings.Contains(err.Error(), "has diverged from origin") {
		t.Fatalf("diverged: %v", err)
	}
	if headOf(o, dir) != mine || read(t, filepath.Join(dir, "local.txt")) != "mine\n" {
		t.Fatal("a diverged branch was changed")
	}
}

func TestSyncRefusesATreeWithTrackedChangesAndLeavesUntrackedAlone(t *testing.T) {
	o := newOrigin(t)
	base := t.TempDir()
	r := repo()
	if _, err := Sync(context.Background(), r, o.opts(base)); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "app")
	// An untracked file never blocks, and is never touched.
	if err := os.WriteFile(filepath.Join(dir, "scratch.txt"), []byte("notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o.commit("b.txt", "two\n", "second")
	o.push("main")
	if res, err := Sync(context.Background(), r, o.opts(base)); err != nil || res.Action != "updated" {
		t.Fatalf("with an untracked file: %+v %v", res, err)
	}
	if read(t, filepath.Join(dir, "scratch.txt")) != "notes\n" {
		t.Fatal("an untracked file was touched")
	}
	// A tracked change blocks the next move and survives it.
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o.commit("c.txt", "three\n", "third")
	o.push("main")
	before := headOf(o, dir)
	_, err := Sync(context.Background(), r, o.opts(base))
	if err == nil || !strings.Contains(err.Error(), "local changes to tracked files") {
		t.Fatalf("dirty: %v", err)
	}
	if headOf(o, dir) != before || read(t, filepath.Join(dir, "a.txt")) != "edited\n" {
		t.Fatal("a dirty tree was changed")
	}
}

func TestSyncTagAndShaAreDetached(t *testing.T) {
	o := newOrigin(t)
	first := o.git(o.work, "rev-parse", "HEAD")
	o.git(o.work, "tag", "v1")
	second := o.commit("b.txt", "two\n", "second")
	o.push("main", "v1")
	base := t.TempDir()
	dir := filepath.Join(base, "app")
	run := func(r config.GitRepo) Result {
		t.Helper()
		res, err := Sync(context.Background(), r, o.opts(base))
		if err != nil {
			t.Fatalf("%+v: %v", r.Refs, err)
		}
		return res
	}
	run(repo(tag("v1")))
	if headOf(o, dir) != first || !detached(dir, o) {
		t.Fatalf("tag: head %s detached %v", headOf(o, dir), detached(dir, o))
	}
	// Move to a commit by sha, from the same clone.
	res := run(repo(sha(second)))
	if headOf(o, dir) != second || !detached(dir, o) || res.Action != "updated" {
		t.Fatalf("sha: %+v head %s", res, headOf(o, dir))
	}
	// An abbreviated sha works.
	run(repo(sha(first[:8])))
	if headOf(o, dir) != first {
		t.Fatalf("abbreviated sha: head %s", headOf(o, dir))
	}
	// And back to the branch.
	run(repo(branch("main")))
	if headOf(o, dir) != second || detached(dir, o) {
		t.Fatalf("branch: head %s", headOf(o, dir))
	}
	// Pinning the commit the branch is on detaches the checkout; the commit is the same.
	if res := run(repo(sha(second))); res.Action != "current" || !detached(dir, o) {
		t.Fatalf("to the sha: %+v detached %v", res, detached(dir, o))
	}
	if res := run(repo(sha(second))); res.Action != "current" {
		t.Fatalf("again: %+v", res)
	}
}

func detached(dir string, o *origin) bool {
	cmd := exec.Command("git", "symbolic-ref", "-q", "HEAD")
	cmd.Dir = dir
	cmd.Env = gitEnv(o.home)
	return cmd.Run() != nil
}

func TestSyncDoesNotMoveATagOrLeaveATreeWithChanges(t *testing.T) {
	o := newOrigin(t)
	o.git(o.work, "tag", "v1")
	o.push("main", "v1")
	base := t.TempDir()
	dir := filepath.Join(base, "app")
	if _, err := Sync(context.Background(), repo(tag("v1")), o.opts(base)); err != nil {
		t.Fatal(err)
	}
	pinned := headOf(o, dir)
	// Origin force-moves the tag: the host refuses to follow it.
	o.commit("b.txt", "two\n", "second")
	o.git(o.work, "tag", "-f", "v1")
	o.push("-f", "main", "v1")
	if _, err := Sync(context.Background(), repo(tag("v1")), o.opts(base)); err == nil {
		t.Fatal("a moved tag was followed")
	}
	if headOf(o, dir) != pinned {
		t.Fatal("a moved tag changed the checkout")
	}
	// A dirty tree is never moved to another ref.
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Sync(context.Background(), repo(branch("main")), o.opts(base)); err == nil || !strings.Contains(err.Error(), "local changes") {
		t.Fatalf("dirty move: %v", err)
	}
	if headOf(o, dir) != pinned || read(t, filepath.Join(dir, "a.txt")) != "edited\n" {
		t.Fatal("a dirty tree was changed")
	}
}

func TestSyncFetchesTheOtherRefs(t *testing.T) {
	o := newOrigin(t)
	o.git(o.work, "tag", "v1")
	o.git(o.work, "checkout", "-q", "-b", "stable")
	o.commit("s.txt", "stable\n", "stable")
	o.push("main", "stable", "v1")
	base := t.TempDir()
	dir := filepath.Join(base, "app")
	res, err := Sync(context.Background(), repo(branch("main"), branch("stable"), tag("v1")), o.opts(base))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(res.Notes, " "), "fetched 2 other refs") {
		t.Errorf("notes = %v", res.Notes)
	}
	// main is checked out; stable and v1 are present but not checked out.
	if _, err := os.Stat(filepath.Join(dir, "s.txt")); !os.IsNotExist(err) {
		t.Error("the second ref was checked out")
	}
	o.git(dir, "rev-parse", "--verify", "refs/remotes/origin/stable")
	o.git(dir, "rev-parse", "--verify", "refs/tags/v1")
}

func TestSyncRefusesWhatItCannotTrust(t *testing.T) {
	o := newOrigin(t)
	base := t.TempDir()
	ctx := context.Background()

	// Not valid: refused before git runs at all.
	ran := 0
	counting := o.opts(base)
	counting.Run = func(ctx context.Context, dir string, env []string, argv ...string) ([]byte, error) {
		ran++
		return nil, errors.New("must not run")
	}
	bad := repo()
	bad.URL = "https://user:token@example.test/acme/app.git"
	if _, err := Sync(ctx, bad, counting); err == nil || !strings.Contains(err.Error(), "must not carry credentials") || ran != 0 {
		t.Fatalf("credentials in the url: %v (ran %d)", err, ran)
	}
	bad = repo(branch("-f"))
	if _, err := Sync(ctx, bad, counting); err == nil || ran != 0 {
		t.Fatalf("a ref that looks like an option: %v (ran %d)", err, ran)
	}
	bad = repo()
	d := "../../escape"
	bad.Dir = d
	if _, err := Sync(ctx, bad, counting); err == nil || ran != 0 {
		t.Fatalf("a dir that leaves the repos directory: %v (ran %d)", err, ran)
	}
	if _, err := os.Stat(filepath.Join(base, "..", "..", "escape")); err == nil {
		t.Fatal("something was created outside the repos directory")
	}

	// Disabled.
	off := false
	dis := repo()
	dis.Enabled = &off
	if _, err := Sync(ctx, dis, o.opts(base)); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled: %v", err)
	}

	// A directory that is not a checkout is left alone.
	if err := os.MkdirAll(filepath.Join(base, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "app", "mine.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Sync(ctx, repo(), o.opts(base)); err == nil || !strings.Contains(err.Error(), "not a git checkout") {
		t.Fatalf("a foreign directory: %v", err)
	}
	if read(t, filepath.Join(base, "app", "mine.txt")) != "x" {
		t.Fatal("a foreign directory was changed")
	}
}

func TestSyncRefusesAClonedElsewhereAndASymlink(t *testing.T) {
	o := newOrigin(t)
	base := t.TempDir()
	ctx := context.Background()
	if _, err := Sync(ctx, repo(), o.opts(base)); err != nil {
		t.Fatal(err)
	}
	other := repo()
	other.URL = "https://example.test/acme/other.git"
	if _, err := Sync(ctx, other, o.opts(base)); err == nil || !strings.Contains(err.Error(), "has origin") {
		t.Fatalf("a clone of another url: %v", err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	// A symbolic link along the way is never followed.
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	viaLink := repo()
	viaLink.Name, viaLink.Dir = "linked", "link/app"
	if _, err := Sync(ctx, viaLink, o.opts(base)); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("through a link: %v", err)
	}
	if des, _ := os.ReadDir(target); len(des) != 0 {
		t.Fatalf("something was written through the link: %v", des)
	}
	atLink := repo()
	atLink.Name, atLink.Dir = "atlink", "link"
	if _, err := Sync(ctx, atLink, o.opts(base)); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("at a link: %v", err)
	}
}

func TestSyncFailsCleanlyAndRemovesWhatItCreated(t *testing.T) {
	o := newOrigin(t)
	base := t.TempDir()
	// The branch does not exist on origin: nothing is left behind.
	_, err := Sync(context.Background(), repo(branch("nope")), o.opts(base))
	if err == nil || !strings.Contains(err.Error(), "fetch") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "app")); !os.IsNotExist(err) {
		t.Fatalf("a failed first sync left %v", err)
	}
	if _, err := Sync(context.Background(), repo(sha("0123456789abcdef0123456789abcdef01234567")), o.opts(base)); err == nil || !strings.Contains(err.Error(), "was not found on origin") {
		t.Fatalf("a commit that is not there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "app")); !os.IsNotExist(err) {
		t.Fatalf("a failed sha sync left %v", err)
	}
}

func TestSyncRunsNoHooks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix hooks")
	}
	o := newOrigin(t)
	base := t.TempDir()
	ctx := context.Background()
	if _, err := Sync(ctx, repo(), o.opts(base)); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "app")
	marker := filepath.Join(t.TempDir(), "hook-ran")
	for _, h := range []string{"post-checkout", "post-merge", "reference-transaction"} {
		p := filepath.Join(dir, ".git", "hooks", h)
		if err := os.WriteFile(p, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	o.commit("b.txt", "two\n", "second")
	o.push("main")
	if res, err := Sync(ctx, repo(), o.opts(base)); err != nil || res.Action != "updated" {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a repository hook ran")
	}
}

func TestSyncHoldsALockWhileItRuns(t *testing.T) {
	o := newOrigin(t)
	base := t.TempDir()
	lock := filepath.Join(base, "app.lock")
	held := 0
	opts := o.opts(base)
	opts.Run = func(ctx context.Context, dir string, env []string, argv ...string) ([]byte, error) {
		if _, err := os.Stat(lock); err == nil {
			held++
		}
		return execRunner(ctx, dir, env, argv...)
	}
	if _, err := Sync(context.Background(), repo(), opts); err != nil {
		t.Fatal(err)
	}
	if held == 0 {
		t.Fatal("no lock was held while git ran")
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Errorf("the lock outlived the sync: %v", err)
	}
}

// ── Credentials ──────────────────────────────────────────────────────────

type call struct {
	argv []string
	env  []string
}

// recorder records every command, answers gh itself and sends git to the real thing.
type recorder struct {
	mu     sync.Mutex
	calls  []call
	ghOK   bool
	execed int
}

func (r *recorder) run(ctx context.Context, dir string, env []string, argv ...string) ([]byte, error) {
	r.mu.Lock()
	r.calls = append(r.calls, call{argv: append([]string(nil), argv...), env: append([]string(nil), env...)})
	r.mu.Unlock()
	if filepath.Base(argv[0]) == "gh" || argv[0] == "gh" {
		if r.ghOK {
			return []byte("ok"), nil
		}
		return nil, errors.New("not signed in")
	}
	r.execed++
	return execRunner(ctx, dir, env, argv...)
}

func (r *recorder) git() []call {
	var out []call
	for _, c := range r.calls {
		if filepath.Base(c.argv[0]) == "git" {
			out = append(out, c)
		}
	}
	return out
}

func hasArg(c call, want string) bool {
	for _, a := range c.argv {
		if a == want {
			return true
		}
	}
	return false
}

func hasEnv(c call, prefix string) bool {
	for _, e := range c.env {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}

func TestPublicRepositoriesUseNoCredentials(t *testing.T) {
	o := newOrigin(t)
	rec := &recorder{}
	opts := o.opts(t.TempDir())
	opts.Run = rec.run
	opts.Getenv = func(k string) string {
		if k == "GH_TOKEN" {
			return "ghp_secret"
		}
		return ""
	}
	t.Setenv("GH_TOKEN", "ghp_secret")
	if _, err := Sync(context.Background(), repo(), opts); err != nil {
		t.Fatal(err)
	}
	for _, c := range rec.git() {
		if !hasArg(c, "credential.helper=") {
			t.Errorf("a git call does not reset the credential helpers: %v", c.argv)
		}
		for _, a := range c.argv {
			if strings.Contains(a, "gh auth") || strings.Contains(a, "ghp_secret") {
				t.Errorf("a credential reached a public repo's argv: %v", c.argv)
			}
		}
		if hasEnv(c, "GH_TOKEN=") || hasEnv(c, "GITHUB_TOKEN=") {
			t.Error("a token variable reached a public repo's git")
		}
		if !hasEnv(c, "GIT_TERMINAL_PROMPT=0") || !hasEnv(c, "GCM_INTERACTIVE=never") {
			t.Error("git may prompt")
		}
		if !hasArg(c, "core.hooksPath="+os.DevNull) {
			t.Errorf("hooks are not off: %v", c.argv)
		}
	}
	for _, c := range rec.calls {
		if filepath.Base(c.argv[0]) == "gh" {
			t.Error("gh was consulted for a public repository")
		}
	}
}

func privateRepo(url string) config.GitRepo {
	id := int64(4242)
	r := repo()
	r.URL, r.Visibility, r.Auth, r.InstallationID = url, "private", "github_app", &id
	return r
}

func TestPrivateHTTPSNeedsTheGitHubCLI(t *testing.T) {
	o := newOrigin(t)
	base := t.TempDir()
	opts := o.opts(base)
	url := "https://github.example.test/acme/private.git"
	// No gh at all.
	opts.LookPath = func(n string) (string, error) {
		if n == "git" {
			return exec.LookPath("git")
		}
		return "", errors.New("not found")
	}
	_, err := Sync(context.Background(), privateRepo(url), opts)
	if err == nil || !strings.Contains(err.Error(), "GitHub CLI, which is not installed") {
		t.Fatalf("no gh: %v", err)
	}
	// gh present but not signed in.
	rec := &recorder{ghOK: false}
	opts.Run = rec.run
	opts.LookPath = func(n string) (string, error) {
		if n == "git" {
			return exec.LookPath("git")
		}
		return "/usr/bin/gh", nil
	}
	_, err = Sync(context.Background(), privateRepo(url), opts)
	if err == nil || !strings.Contains(err.Error(), "not signed in to github.example.test") || rec.execed != 0 {
		t.Fatalf("gh not signed in: %v (git ran %d times)", err, rec.execed)
	}
	if _, statErr := os.Stat(filepath.Join(base, "app")); !os.IsNotExist(statErr) {
		t.Error("a refused private sync left a directory behind")
	}
}

func TestPrivateHTTPSUsesTheCLIHelperAndNeverATokenInTheURL(t *testing.T) {
	o := newOrigin(t)
	rec := &recorder{ghOK: true}
	opts := o.opts(t.TempDir())
	opts.Run = rec.run
	opts.LookPath = func(n string) (string, error) {
		if n == "git" {
			return exec.LookPath("git")
		}
		return "/usr/bin/gh", nil
	}
	opts.Getenv = func(k string) string {
		if k == "GH_TOKEN" {
			return "ghp_secret"
		}
		return ""
	}
	// The url is rewritten to the local origin by the test environment, which also
	// means git never contacts a real host. The point here is the argv and env.
	r := privateRepo(testURL)
	if _, err := Sync(context.Background(), r, opts); err != nil {
		t.Fatal(err)
	}
	var sawHelper, sawToken bool
	for _, c := range rec.git() {
		for _, a := range c.argv {
			if a == "credential.https://example.test.helper=!gh auth git-credential" {
				sawHelper = true
			}
			if strings.Contains(a, "ghp_secret") || strings.Contains(a, "@example.test") {
				t.Errorf("a token reached a git argv: %v", c.argv)
			}
		}
		if hasEnv(c, "GH_TOKEN=ghp_secret") {
			sawToken = true
		}
	}
	if !sawHelper || !sawToken {
		t.Errorf("helper %v, token passed through to the helper %v", sawHelper, sawToken)
	}
	// gh was asked about the right host.
	var asked bool
	for _, c := range rec.calls {
		if filepath.Base(c.argv[0]) == "gh" && strings.Join(c.argv[1:], " ") == "auth status --hostname example.test" {
			asked = true
		}
	}
	if !asked {
		t.Errorf("gh was not asked about the host: %v", rec.calls)
	}
}

func TestPrivateSSHUsesBatchModeAndNoHelper(t *testing.T) {
	o := newOrigin(t)
	rec := &recorder{}
	opts := o.opts(t.TempDir())
	opts.Run = rec.run
	opts.Getenv = func(string) string { return "" }
	r := privateRepo("git@example.test:acme/app.git")
	opts.Env = append(opts.Env, "GIT_CONFIG_KEY_0=url."+o.bare+".insteadOf", "GIT_CONFIG_VALUE_0=git@example.test:acme/app.git")
	if _, err := Sync(context.Background(), r, opts); err != nil {
		t.Fatal(err)
	}
	for _, c := range rec.calls {
		if filepath.Base(c.argv[0]) == "gh" {
			t.Error("gh was consulted for an ssh url")
		}
	}
	if len(rec.git()) == 0 || !hasEnv(rec.git()[0], "GIT_SSH_COMMAND=ssh -o BatchMode=yes") {
		t.Error("ssh may prompt")
	}
	// The user's own GIT_SSH_COMMAND is respected.
	rec2 := &recorder{}
	opts.Run = rec2.run
	opts.Getenv = func(k string) string {
		if k == "GIT_SSH_COMMAND" {
			return "ssh -i /my/key"
		}
		return ""
	}
	if _, err := Sync(context.Background(), r, opts); err != nil {
		t.Fatal(err)
	}
	for _, c := range rec2.git() {
		if hasEnv(c, "GIT_SSH_COMMAND=ssh -o BatchMode=yes") {
			t.Error("the user's ssh command was replaced")
		}
	}
}

// ── Status ───────────────────────────────────────────────────────────────

func TestStatusReadsTheCloneWithoutFetching(t *testing.T) {
	o := newOrigin(t)
	base := t.TempDir()
	ctx := context.Background()
	r := repo()
	st := StatusOf(ctx, r, o.opts(base))
	if st.State != "not cloned" || st.Name != "app" {
		t.Fatalf("before a clone: %+v", st)
	}
	if _, err := Sync(ctx, r, o.opts(base)); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "app")
	if st := StatusOf(ctx, r, o.opts(base)); st.State != "current" || st.Dirty || len(st.Commit) != 12 {
		t.Fatalf("after a sync: %+v", st)
	}
	// Origin moves, and a plain fetch (not a sync) shows the clone is behind.
	o.commit("b.txt", "two\n", "second")
	o.push("main")
	if st := StatusOf(ctx, r, o.opts(base)); st.State != "current" {
		t.Fatalf("status must not fetch: %+v", st)
	}
	o.git(dir, "-c", "url."+o.bare+".insteadOf="+testURL, "fetch", "-q", "origin", "+refs/heads/main:refs/remotes/origin/main")
	st = StatusOf(ctx, r, o.opts(base))
	if st.State != "behind" || !strings.HasPrefix(st.Detail, "1 commit behind") {
		t.Fatalf("behind: %+v", st)
	}
	// A local commit makes it diverged.
	if err := os.WriteFile(filepath.Join(dir, "l.txt"), []byte("l"), 0o644); err != nil {
		t.Fatal(err)
	}
	o.git(dir, "add", "l.txt")
	o.git(dir, "commit", "-q", "-m", "local")
	if st := StatusOf(ctx, r, o.opts(base)); st.State != "diverged" || !strings.Contains(st.Detail, "1 ahead, 1 behind") {
		t.Fatalf("diverged: %+v", st)
	}
	// Dirty is reported beside the state.
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st := StatusOf(ctx, r, o.opts(base)); !st.Dirty {
		t.Fatalf("dirty: %+v", st)
	}
	// Disabled and invalid never touch git.
	off := false
	dis := r
	dis.Enabled = &off
	if st := StatusOf(ctx, dis, o.opts(base)); st.State != "disabled" {
		t.Fatalf("disabled: %+v", st)
	}
	bad := r
	bad.URL = "ftp://x"
	if st := StatusOf(ctx, bad, o.opts(base)); st.State != "invalid" {
		t.Fatalf("invalid: %+v", st)
	}
}

func TestStatusForTagsAndOtherRefs(t *testing.T) {
	o := newOrigin(t)
	o.git(o.work, "tag", "v1")
	o.push("main", "v1")
	base := t.TempDir()
	ctx := context.Background()
	if _, err := Sync(ctx, repo(tag("v1")), o.opts(base)); err != nil {
		t.Fatal(err)
	}
	if st := StatusOf(ctx, repo(tag("v1")), o.opts(base)); st.State != "current" {
		t.Fatalf("tag: %+v", st)
	}
	// The same clone asked about the branch is on another ref.
	if st := StatusOf(ctx, repo(branch("main")), o.opts(base)); st.State != "other ref" || !strings.Contains(st.Detail, "detached") {
		t.Fatalf("branch while detached: %+v", st)
	}
	if st := StatusOf(ctx, repo(tag("v9")), o.opts(base)); st.State != "not fetched" {
		t.Fatalf("a tag never fetched: %+v", st)
	}
}

func TestResolveDir(t *testing.T) {
	base := t.TempDir()
	r := repo()
	got, err := ResolveDir(base, r)
	if err != nil || got != filepath.Join(base, "app") {
		t.Fatalf("%q %v", got, err)
	}
	r.Dir = "team/app"
	if got, err := ResolveDir(base, r); err != nil || got != filepath.Join(base, "team", "app") {
		t.Fatalf("nested: %q %v", got, err)
	}
	for _, bad := range []string{"../x", "/abs", "a/../../b", "~/x", "a//b"} {
		r.Dir = bad
		if _, err := ResolveDir(base, r); err == nil {
			t.Errorf("dir %q resolved", bad)
		}
	}
	if _, err := ResolveDir("", repo()); err == nil {
		t.Error("an empty base resolved")
	}
}
