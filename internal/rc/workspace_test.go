package rc

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/forge"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// wsGit runs git for a fixture and fails the test on error.
func wsGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// wsFixture is a bare origin with main and a branch only it has, a clone of
// it on main (the offered directory), and an empty worktrees directory.
type wsFixture struct {
	origin, repo, root string
	trusted            []string
}

func newWSFixture(t *testing.T) *wsFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	base, _ := Normalize(t.TempDir())
	f := &wsFixture{origin: filepath.Join(base, "origin.git"), repo: filepath.Join(base, "app"), root: filepath.Join(base, "worktrees")}
	wsGit(t, base, "init", "--bare", "-b", "main", f.origin)
	seed := filepath.Join(base, "seed")
	wsGit(t, base, "clone", f.origin, seed)
	wsGit(t, seed, "checkout", "-b", "main")
	if err := os.WriteFile(filepath.Join(seed, "a.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wsGit(t, seed, "add", "a.txt")
	wsGit(t, seed, "commit", "-m", "first")
	wsGit(t, seed, "push", "-u", "origin", "main")
	wsGit(t, seed, "checkout", "-b", "remote-only")
	wsGit(t, seed, "commit", "--allow-empty", "-m", "remote work")
	wsGit(t, seed, "push", "origin", "remote-only")
	wsGit(t, f.origin, "symbolic-ref", "HEAD", "refs/heads/main")
	wsGit(t, base, "clone", f.origin, f.repo)
	wsGit(t, f.repo, "branch", "feat/x")
	return f
}

// prep is the daemon's preparation with this fixture's worktrees directory.
// The origin is a local path, which the hardened runner's no-file-transport
// rule would refuse, so the test's runner allows it; everything else is the
// hardened runner.
func (f *wsFixture) prep() workspacePrep {
	hard := forge.HardenedGit("", "", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
	return workspacePrep{
		run: func(ctx context.Context, dir string, argv ...string) ([]byte, error) {
			if len(argv) > 0 && argv[0] == "git" {
				argv = append([]string{"git", "-c", "protocol.file.allow=always"}, argv[1:]...)
			}
			return hard(ctx, dir, argv...)
		},
		root:  func() (string, error) { return f.root, nil },
		trust: func(dir string) error { f.trusted = append(f.trusted, dir); return nil },
	}
}

func TestPrepareWorkspaceStaysInCwdWhenNothingMoves(t *testing.T) {
	f := newWSFixture(t)
	ctx := context.Background()
	for _, c := range [][2]string{{"", ""}, {"main", ""}} {
		dir, err := f.prep().prepare(ctx, f.repo, c[0], c[1])
		if err != nil || dir != f.repo {
			t.Fatalf("prepare(%q, %q) = %q, %v", c[0], c[1], dir, err)
		}
	}
	if len(f.trusted) != 0 {
		t.Fatalf("trusted %v without a worktree", f.trusted)
	}
}

// A local branch gets a worktree at the default workspace (the checkout's
// name when origin is no forge address), trusted, and a second start reuses it.
func TestPrepareWorkspaceMakesAndReusesAWorktree(t *testing.T) {
	f := newWSFixture(t)
	ctx := context.Background()
	dir, err := f.prep().prepare(ctx, f.repo, "feat/x", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(f.root, "app", "feat-x"); dir != want {
		t.Fatalf("dir = %q, want %q", dir, want)
	}
	if got := wsGit(t, dir, "branch", "--show-current"); got != "feat/x" {
		t.Fatalf("worktree on %q", got)
	}
	if len(f.trusted) != 1 || f.trusted[0] != dir {
		t.Fatalf("trusted = %v", f.trusted)
	}
	again, err := f.prep().prepare(ctx, f.repo, "feat/x", "app/feat-x")
	if err != nil || again != dir {
		t.Fatalf("reuse = %q, %v", again, err)
	}
	// A workspace alone takes whatever branch it is on.
	if again, err := f.prep().prepare(ctx, f.repo, "", "app/feat-x"); err != nil || again != dir {
		t.Fatalf("workspace alone = %q, %v", again, err)
	}
	// Another branch in the same workspace is refused with what is there.
	wsGit(t, f.repo, "branch", "other")
	if _, err := f.prep().prepare(ctx, f.repo, "other", "app/feat-x"); err == nil || err.Error() != "workspace app/feat-x is on branch feat/x" {
		t.Fatalf("other branch: %v", err)
	}
}

// The default workspace is <owner>/<repo>/<branch slug> for a forge origin.
func TestDefaultWorkspaceNamesTheForgeRepository(t *testing.T) {
	f := newWSFixture(t)
	wsGit(t, f.repo, "remote", "set-url", "origin", "https://github.com/acme/app.git")
	if got := defaultWorkspace(f.repo, "feat/x"); got != "acme/app/feat-x" {
		t.Fatalf("default = %q", got)
	}
	if got := defaultWorkspace(t.TempDir()+"/my repo!", "b"); got != "my-repo/b" {
		t.Fatalf("fallback = %q", got)
	}
}

// A branch only origin has is fetched and the worktree tracks it.
func TestPrepareWorkspaceFetchesABranchOnlyOriginHas(t *testing.T) {
	f := newWSFixture(t)
	dir, err := f.prep().prepare(context.Background(), f.repo, "remote-only", "fetched")
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(f.root, "fetched") {
		t.Fatalf("dir = %q", dir)
	}
	if up := wsGit(t, dir, "rev-parse", "--abbrev-ref", "@{u}"); up != "origin/remote-only" {
		t.Fatalf("upstream = %q", up)
	}
	if _, err := f.prep().prepare(context.Background(), f.repo, "nowhere", "n"); err == nil || !strings.Contains(err.Error(), "could not be fetched") {
		t.Fatalf("missing branch: %v", err)
	}
}

func TestPrepareWorkspaceRefusals(t *testing.T) {
	f := newWSFixture(t)
	ctx := context.Background()
	for _, c := range []struct{ branch, ws, want string }{
		{"-x", "", "that is not a branch name"},
		{"a..b", "", "that is not a branch name"},
		{"x.lock", "", "invalid branch name"},
		{"feat/x", "../escape", "that is not a workspace name"},
		{"feat/x", "a/./b", "that is not a workspace name"},
		{"feat/x", "/abs", "that is not a workspace name"},
		{"feat/x", "a/b/c/d/e", "that is not a workspace name"},
		{"feat/x", strings.Repeat("a", 201), "that is not a workspace name"},
		// Git's own refusal: main is checked out in the directory itself.
		{"main", "elsewhere", "git refused to make the worktree"},
		{"", "missing", "does not exist; name a branch"},
	} {
		if _, err := f.prep().prepare(ctx, f.repo, c.branch, c.ws); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("prepare(%q, %q) = %v, want %q", c.branch, c.ws, err, c.want)
		}
	}
	// A directory that is there but is no worktree of this repository.
	if err := os.MkdirAll(filepath.Join(f.root, "plain"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := f.prep().prepare(ctx, f.repo, "feat/x", "plain"); err == nil || !strings.Contains(err.Error(), "not a worktree of this repository") {
		t.Fatalf("plain dir: %v", err)
	}
	// Another repository's worktree under the same root.
	other := filepath.Join(t.TempDir(), "other")
	wsGit(t, filepath.Dir(other), "clone", f.origin, other)
	wsGit(t, other, "worktree", "add", "-b", "theirs", filepath.Join(f.root, "theirs"))
	if _, err := f.prep().prepare(ctx, f.repo, "theirs", "theirs"); err == nil || !strings.Contains(err.Error(), "not a worktree of this repository") {
		t.Fatalf("another repository's worktree: %v", err)
	}
	// A symlink out of the worktrees directory.
	if err := os.Symlink(t.TempDir(), filepath.Join(f.root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.prep().prepare(ctx, f.repo, "feat/x", "link/inner"); err == nil || !strings.Contains(err.Error(), "outside the worktrees directory") {
		t.Fatalf("symlink: %v", err)
	}
	// Not a checkout at all.
	if _, err := f.prep().prepare(ctx, t.TempDir(), "feat/x", ""); err == nil || !strings.Contains(err.Error(), "not a git checkout") {
		t.Fatalf("plain cwd: %v", err)
	}
	if len(f.trusted) != 0 {
		t.Fatalf("a refusal trusted %v", f.trusted)
	}
}

func TestValidWorkspace(t *testing.T) {
	for ws, want := range map[string]bool{
		"a": true, "acme/app/feat-x": true, "a/b/c/d": true, "a.b_c-d": true,
		"": false, "a/b/c/d/e": false, ".": false, "..": false, "a/..": false, "a//b": false,
		"/a": false, "a/": false, "a b": false, "a\\b": false,
	} {
		if got := ValidWorkspace(ws); got != want {
			t.Errorf("ValidWorkspace(%q) = %v", ws, got)
		}
	}
}

// The advertisement lists only worktrees under the worktrees directory, by a
// path rebuilt from the checked workspace, and never the directory itself.
func TestListWorktreesKeepsOnlyThoseUnderTheRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "wt")
	list := []forge.Worktree{
		{Path: "/src/app", Branch: "main"},
		{Path: filepath.Join(root, "acme/app/feat-x"), Branch: "feat/x"},
		{Path: filepath.Join(root, "acme/app/odd"), Branch: "has space"},
		{Path: filepath.Join(root, "gone"), Branch: "g", Prunable: true},
		{Path: filepath.Join(root, "a/b/c/d/e"), Branch: "deep"},
		{Path: "/elsewhere/tp-1", Branch: "teleport/x"},
	}
	got := listWorktrees(list, root, "/src/app")
	want := []sessionsync.RCWorktree{
		{Path: filepath.Join(root, "acme/app/feat-x"), Branch: "feat/x", Workspace: "acme/app/feat-x"},
		{Path: filepath.Join(root, "acme/app/odd"), Workspace: "acme/app/odd"},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("worktrees = %+v", got)
	}
	var many []forge.Worktree
	for i := range sessionsync.RCMaxWorktrees + 5 {
		many = append(many, forge.Worktree{Path: filepath.Join(root, "w"+string(rune('a'+i%26))+strings.Repeat("x", i/26)), Branch: "b"})
	}
	if n := len(listWorktrees(many, root, "/src/app")); n != sessionsync.RCMaxWorktrees {
		t.Fatalf("listed %d", n)
	}
}

// The daemon starts the session in the directory the preparation returns,
// passes its refusal on, and leaves a pix-assistant session where it was.
func TestStartRunsInThePreparedWorkspace(t *testing.T) {
	d, proj, children, _ := agentDaemon(t, nil)
	t.Cleanup(d.wg.Wait)
	real, _ := Normalize(proj)
	var asked [][3]string
	d.o.Workspace = func(_ context.Context, cwd, branch, ws string) (string, error) {
		asked = append(asked, [3]string{cwd, branch, ws})
		if branch == "busy" {
			return "", errors.New("workspace w is on branch other")
		}
		return "/wt/acme/app/feat-x", nil
	}
	if _, why := d.start(sessionsync.Dispatch{ID: "d1", Kind: "start", Cwd: proj, Prompt: "hi", Branch: "feat/x"}); why != "" {
		t.Fatalf("start refused: %q", why)
	}
	if got := (*children)[0].Cwd; got != "/wt/acme/app/feat-x" {
		t.Fatalf("child cwd = %q", got)
	}
	if len(asked) != 1 || asked[0] != [3]string{real, "feat/x", ""} {
		t.Fatalf("asked = %v", asked)
	}
	if !d.worktreesChanged {
		t.Fatal("a worktree start did not ask for a new advertisement")
	}
	if _, why := d.start(sessionsync.Dispatch{ID: "d2", Kind: "start", Cwd: proj, Prompt: "hi", Branch: "busy", Workspace: "w"}); why != "workspace w is on branch other" {
		t.Fatalf("refusal = %q", why)
	}
	if _, why := d.start(sessionsync.Dispatch{ID: "d3", Kind: "start", Cwd: proj, Prompt: "hi", Mode: "pix-assistant", Profile: "vuln-scout", Branch: "feat/x"}); why != "" {
		t.Fatalf("pix start: %q", why)
	}
	if len(asked) != 2 || (*children)[1].Cwd != real {
		t.Fatalf("a pix-assistant start prepared a workspace: %v, cwd %q", asked, (*children)[1].Cwd)
	}
}

// A worktree a start made is in the directory's next advertisement.
func TestDirWorktreesReadsTheRepository(t *testing.T) {
	f := newWSFixture(t)
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("BELAI_WORKTREES_DIR", f.root)
	dir, err := f.prep().prepare(context.Background(), f.repo, "feat/x", "")
	if err != nil {
		t.Fatal(err)
	}
	got := dirWorktrees(context.Background(), f.repo)
	if len(got) != 1 || got[0] != (sessionsync.RCWorktree{Path: dir, Branch: "feat/x", Workspace: "app/feat-x"}) {
		t.Fatalf("worktrees = %+v", got)
	}
	if got := dirWorktrees(context.Background(), t.TempDir()); got != nil {
		t.Fatalf("a plain directory listed %+v", got)
	}
}
