package rc

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/repos"
)

func TestDecideAdopt(t *testing.T) {
	base := "/wt/api-1234"
	clone := "/src/api"
	f := func(mod func(*adoptFacts)) adoptFacts {
		x := adoptFacts{Clone: clone, Default: "main", Designated: "main", Branch: "main", Rel: relSame, Remote: true, Base: base}
		if mod != nil {
			mod(&x)
		}
		return x
	}
	for _, c := range []struct {
		name string
		f    adoptFacts
		want adoptPlan
		why  string
	}{
		{"clean main, level", f(nil),
			adoptPlan{Action: actUseClone, Path: clone}, "level with origin"},
		{"clean main, behind", f(func(x *adoptFacts) { x.Rel = relBehind }),
			adoptPlan{Action: actUseClone, Path: clone, FastForward: "refs/remotes/origin/main"}, "fast-forwarded"},
		{"clean main, ahead", f(func(x *adoptFacts) { x.Rel = relAhead }),
			adoptPlan{Action: actNew, Path: base + "/rc-main", Detach: true, Start: "refs/remotes/origin/main"}, "local commits"},
		{"clean main, diverged", f(func(x *adoptFacts) { x.Rel = relDiverged }),
			adoptPlan{Action: actNew, Path: base + "/rc-main", Detach: true, Start: "refs/remotes/origin/main"}, "diverged"},
		{"dirty main", f(func(x *adoptFacts) { x.Dirty = true }),
			adoptPlan{Action: actNew, Path: base + "/rc-main", Detach: true, Start: "refs/remotes/origin/main"}, "main is dirty, so a detached worktree"},
		{"dirty main, clean detached worktree of ours is reused", f(func(x *adoptFacts) {
			x.Dirty = true
			x.Worktrees = []adoptWorktree{{Path: base + "/rc-main", Ours: true, Rel: relSame}}
			x.Taken = []string{"rc-main"}
		}), adoptPlan{Action: actReuse, Path: base + "/rc-main", Detach: true}, "is reused"},
		{"dirty main, behind detached worktree of ours is moved", f(func(x *adoptFacts) {
			x.Dirty = true
			x.Worktrees = []adoptWorktree{{Path: base + "/rc-main", Ours: true, Rel: relBehind}}
			x.Taken = []string{"rc-main"}
		}), adoptPlan{Action: actReuse, Path: base + "/rc-main", Detach: true, FastForward: "refs/remotes/origin/main"}, "moved"},
		{"dirty main, our worktree is dirty too", f(func(x *adoptFacts) {
			x.Dirty = true
			x.Worktrees = []adoptWorktree{{Path: base + "/rc-main", Ours: true, Dirty: true, Rel: relSame}}
			x.Taken = []string{"rc-main"}
		}), adoptPlan{Action: actNew, Path: base + "/rc-main-2", Detach: true, Start: "refs/remotes/origin/main"}, ""},
		{"dirty main, a detached worktree with commits of its own is not reused", f(func(x *adoptFacts) {
			x.Dirty = true
			x.Worktrees = []adoptWorktree{{Path: base + "/rc-main", Ours: true, Rel: relAhead}}
			x.Taken = []string{"rc-main"}
		}), adoptPlan{Action: actNew, Path: base + "/rc-main-2", Detach: true, Start: "refs/remotes/origin/main"}, ""},
		{"dirty main, a detached worktree that is not ours is not reused", f(func(x *adoptFacts) {
			x.Dirty = true
			x.Worktrees = []adoptWorktree{{Path: "/home/u/.vulnetix/worktrees/api-1/tp-1", Rel: relSame}}
		}), adoptPlan{Action: actNew, Path: base + "/rc-main", Detach: true, Start: "refs/remotes/origin/main"}, ""},
		{"clone on a feature branch", f(func(x *adoptFacts) { x.Branch = "feat/x"; x.Rel = relNone }),
			adoptPlan{Action: actNew, Path: base + "/rc-main", Detach: true, Start: "refs/remotes/origin/main"}, "is on feat/x, not main"},
		{"clone detached", f(func(x *adoptFacts) { x.Branch = ""; x.Rel = relNone }),
			adoptPlan{Action: actNew, Path: base + "/rc-main", Detach: true, Start: "refs/remotes/origin/main"}, "detached HEAD"},
		{"another branch, on origin only", f(func(x *adoptFacts) { x.Designated = "release/1.2" }),
			adoptPlan{Action: actNew, Path: base + "/rc-release-1.2", Branch: "release/1.2", Start: "refs/remotes/origin/release/1.2", NewBranch: true, Track: true}, "tracking origin's release/1.2"},
		{"another branch, nowhere yet", f(func(x *adoptFacts) { x.Designated = "belai/fix"; x.Remote = false }),
			adoptPlan{Action: actNew, Path: base + "/rc-belai-fix", Branch: "belai/fix", Start: "refs/remotes/origin/main", NewBranch: true}, "new branch from main"},
		{"another branch, local and behind", f(func(x *adoptFacts) { x.Designated = "dev"; x.Local = true; x.LocalRel = relBehind }),
			adoptPlan{Action: actNew, Path: base + "/rc-dev", Branch: "dev", FastForward: "refs/remotes/origin/dev"}, "fast-forwarded"},
		{"another branch, local and ahead is never reset", f(func(x *adoptFacts) { x.Designated = "dev"; x.Local = true; x.LocalRel = relAhead }),
			adoptPlan{Action: actNew, Path: base + "/rc-dev", Branch: "dev"}, "left as it is"},
		{"another branch, local only", f(func(x *adoptFacts) { x.Designated = "dev"; x.Local = true; x.Remote = false }),
			adoptPlan{Action: actNew, Path: base + "/rc-dev", Branch: "dev"}, "local branch"},
		{"another branch, a clean worktree has it", f(func(x *adoptFacts) {
			x.Designated = "dev"
			x.Local = true
			x.Worktrees = []adoptWorktree{{Path: "/elsewhere/dev", Branch: "dev", Rel: relBehind}}
		}), adoptPlan{Action: actReuse, Path: "/elsewhere/dev", FastForward: "refs/remotes/origin/dev"}, "is reused and fast-forwarded"},
		{"another branch, its worktree is dirty", f(func(x *adoptFacts) {
			x.Designated = "dev"
			x.Local = true
			x.LocalRel = relSame
			x.Worktrees = []adoptWorktree{{Path: "/elsewhere/dev", Branch: "dev", Dirty: true}}
		}), adoptPlan{Action: actNew, Path: base + "/rc-dev", Detach: true, Start: "refs/remotes/origin/dev"}, "checked out at /elsewhere/dev"},
		{"another branch, the clone has it checked out", f(func(x *adoptFacts) {
			x.Designated = "dev"
			x.Branch = "dev"
			x.Local = true
			x.LocalRel = relAhead
		}), adoptPlan{Action: actNew, Path: base + "/rc-dev", Detach: true, Start: "refs/heads/dev"}, "checked out at /src/api"},
		{"a branch nowhere here while origin is unreachable falls back to the default", f(func(x *adoptFacts) {
			x.Designated = "feat/x"
			x.Remote = false
			x.Stale = "timed out"
			x.Fallback = true
		}), adoptPlan{Action: actNew, Path: base + "/rc-main", Detach: true, Start: "refs/remotes/origin/main"}, "feat/x is not here and could not be fetched"},
		{"too many worktrees", f(func(x *adoptFacts) {
			x.Dirty = true
			for n := 1; n <= maxRCWorktrees; n++ {
				x.Taken = append(x.Taken, rcName("main", n))
			}
		}), adoptPlan{Err: "too many worktrees"}, ""},
	} {
		got := decideAdopt(c.f)
		why, err := got.Why, got.Err
		got.Why, got.Err = "", ""
		want := c.want
		wantErr := want.Err
		want.Err = ""
		if got != want {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, want)
		}
		if wantErr != "" && !strings.Contains(err, wantErr) {
			t.Errorf("%s: err %q, want %q", c.name, err, wantErr)
		}
		if wantErr == "" && err != "" {
			t.Errorf("%s: unexpected err %q", c.name, err)
		}
		if !strings.Contains(why, c.why) {
			t.Errorf("%s: why %q, want %q", c.name, why, c.why)
		}
	}
}

func TestPickClone(t *testing.T) {
	user := adoptCand{Path: "/src/api", Primary: true}
	other := adoptCand{Path: "/src/api-2", Primary: true}
	for _, c := range []struct {
		name    string
		offered []adoptCand
		hint    string
		belai   string
		mains   []string
		want    string
	}{
		{"primary first", []adoptCand{user, other}, "", "/wt/repos/acme/api", nil, "/src/api"},
		{"hint wins among primaries", []adoptCand{user, other}, "/src/api-2", "", nil, "/src/api-2"},
		{"a hint that is not a candidate is ignored", []adoptCand{user}, "/tmp/x", "", nil, "/src/api"},
		{"belai's clone before a worktree's repository", nil, "", "/wt/repos/acme/api", []string{"/src/api"}, "/wt/repos/acme/api"},
		{"a worktree's repository last", nil, "", "", []string{"/src/api"}, "/src/api"},
		{"none", nil, "", "", nil, ""},
	} {
		if got, _ := pickClone(c.offered, c.hint, c.belai, c.mains); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestRCNames(t *testing.T) {
	for name, want := range map[string]bool{"rc-main": true, "rc-main-2": true, "rc-main-10": true, "rc-main-1": false, "rc-main-02": false, "rc-main-x": false, "tp-main": false, "rc-mainline": false} {
		if got := isRCName(name, "main"); got != want {
			t.Errorf("isRCName(%q) = %v", name, got)
		}
	}
	if branchSlug("feat/a b") != "feat-a-b" {
		t.Errorf("slug = %q", branchSlug("feat/a b"))
	}
}

// ── Real git ─────────────────────────────────────────────────────────────

const adoptURL = "https://github.com/acme/api.git"

// adoptFix is an origin (a bare repository reached through a url rewrite, so
// every checkout's origin is the GitHub url), a place for the user's clones and
// a worktrees directory, with an adopter pointed at them.
type adoptFix struct {
	t                    *testing.T
	root, bare, work, wt string
	env                  []string
	a                    *adopter
	trusted              []string
	clones               int
}

func newAdoptFix(t *testing.T) *adoptFix {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("BELAI_HOME", t.TempDir())
	root, err := Normalize(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &adoptFix{t: t, root: root, bare: filepath.Join(root, "origin.git"), work: filepath.Join(root, "work"), wt: filepath.Join(root, "worktrees")}
	home := filepath.Join(root, "home")
	for _, d := range []string{home, f.work} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	pin := []string{"HOME=" + home, "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_SYSTEM=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_ALLOW_PROTOCOL=file:https:ssh",
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=url." + f.bare + ".insteadOf", "GIT_CONFIG_VALUE_0=" + adoptURL}
	f.env = append(append(os.Environ(), pin...),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test", "LC_ALL=C")
	f.git(root, "init", "-q", "--bare", "-b", "main", f.bare)
	f.git(f.work, "init", "-q", "-b", "main")
	f.git(f.work, "remote", "add", "origin", f.bare)
	f.commit(f.work, "a.txt", "one\n", "first")
	f.git(f.work, "push", "-q", "origin", "main")
	opts := repos.Options{GitHubCLI: true, Env: pin,
		LookPath: func(n string) (string, error) {
			if n == "git" {
				return exec.LookPath("git")
			}
			return "", errors.New("not found")
		}}
	f.a = &adopter{opts: opts,
		root:  func() (string, error) { return f.wt, nil },
		trust: func(dir string) error { f.trusted = append(f.trusted, dir); return nil },
		clone: func(ctx context.Context, t CloneTarget, ref string) (string, error) {
			f.clones++
			o := opts
			o.ReposDir = filepath.Join(f.wt, "repos")
			return cloneWith(ctx, t, ref, o, func(string) error { return nil })
		},
	}
	return f
}

func (f *adoptFix) git(dir string, args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = dir, f.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *adoptFix) commit(dir, name, body, msg string) string {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
	f.git(dir, "add", name)
	f.git(dir, "commit", "-q", "-m", msg)
	return f.git(dir, "rev-parse", "HEAD")
}

// advance commits on origin's branch and returns the new tip.
func (f *adoptFix) advance(branch, name string) string {
	f.t.Helper()
	f.git(f.work, "checkout", "-q", branch)
	tip := f.commit(f.work, name, name+"\n", "add "+name)
	f.git(f.work, "push", "-q", "origin", branch)
	return tip
}

// userClone clones origin to the user's own place, as `git clone` would.
func (f *adoptFix) userClone(name string) string {
	f.t.Helper()
	dir := filepath.Join(f.root, "src", name)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		f.t.Fatal(err)
	}
	f.git(filepath.Dir(dir), "clone", "-q", adoptURL, dir)
	return dir
}

func (f *adoptFix) adopt(ref, hint string, offered ...string) adoptResult {
	f.t.Helper()
	res, err := f.try(ref, hint, offered...)
	if err != nil {
		f.t.Fatalf("adopt %q: %v", ref, err)
	}
	return res
}

func (f *adoptFix) try(ref, hint string, offered ...string) (adoptResult, error) {
	var dirs []Dir
	for _, p := range offered {
		dirs = append(dirs, Dir{Path: p, Name: filepath.Base(p), Source: SourceTrusted})
	}
	return f.a.adopt(context.Background(), adoptRequest{Owner: "acme", Name: "api", Ref: ref, Hint: hint, Offered: dirs, CanClone: true})
}

func (f *adoptFix) head(dir string) string { return f.git(dir, "rev-parse", "HEAD") }

func (f *adoptFix) branch(dir string) string {
	f.t.Helper()
	cmd := exec.Command("git", "symbolic-ref", "-q", "--short", "HEAD")
	cmd.Dir, cmd.Env = dir, f.env
	out, _ := cmd.Output()
	return strings.TrimSpace(string(out))
}

// With nothing to adopt the default branch is cloned once; the next request
// adopts the clone, offered or not.
func TestAdoptClonesOnceThenAdopts(t *testing.T) {
	f := newAdoptFix(t)
	first := f.adopt("", "")
	want := filepath.Join(f.wt, "repos", "acme", "api")
	if !first.Cloned || first.Clone != want || first.Path != want || f.clones != 1 {
		t.Fatalf("first = %+v, clones %d", first, f.clones)
	}
	if f.branch(want) != "main" {
		t.Errorf("the clone is on %q", f.branch(want))
	}
	if len(f.trusted) != 1 || f.trusted[0] != want {
		t.Errorf("trusted %v", f.trusted)
	}
	if originHead(want) != "main" {
		t.Errorf("origin's HEAD was not recorded in Belai's clone: %q", originHead(want))
	}
	second := f.adopt("main", "")
	if second.Cloned || second.Path != want || f.clones != 1 || !strings.Contains(second.Why, "Belai's clone") {
		t.Fatalf("second = %+v, clones %d", second, f.clones)
	}
	// Without the clone and without canClone, the request is refused.
	f2 := newAdoptFix(t)
	if _, err := f2.a.adopt(context.Background(), adoptRequest{Owner: "acme", Name: "api"}); err == nil || !strings.Contains(err.Error(), "does not clone") {
		t.Errorf("no clone: %v", err)
	}
}

// A clean clone on the default branch is used as it is, fast-forwarded when
// origin moved.
func TestAdoptCleanMainUsesTheCloneAndFastForwards(t *testing.T) {
	f := newAdoptFix(t)
	user := f.userClone("api")
	tip := f.advance("main", "b.txt")
	res := f.adopt("", user, user)
	if res.Path != user || res.Cloned || f.clones != 0 {
		t.Fatalf("res = %+v", res)
	}
	if f.head(user) != tip || f.branch(user) != "main" {
		t.Errorf("not fast-forwarded: head %s branch %s", f.head(user), f.branch(user))
	}
	if !strings.Contains(res.Why, "fast-forwarded") {
		t.Errorf("why %q", res.Why)
	}
	if entries, _ := os.ReadDir(f.wt); len(entries) != 0 {
		t.Errorf("a worktree was made: %v", entries)
	}
}

// A dirty default branch is never touched: the workers get a detached worktree
// at origin's tip, reused while it is clean, and a new one once it is dirty.
func TestAdoptDirtyMainUsesADetachedWorktree(t *testing.T) {
	f := newAdoptFix(t)
	user := f.userClone("api")
	before := f.head(user)
	if err := os.WriteFile(filepath.Join(user, "a.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tip := f.advance("main", "b.txt")
	res := f.adopt("", user, user)
	if res.Path == user || !strings.HasPrefix(res.Path, f.wt+string(filepath.Separator)) || filepath.Base(res.Path) != "rc-main" {
		t.Fatalf("res = %+v", res)
	}
	if !strings.Contains(res.Why, "main is dirty, so a detached worktree at") {
		t.Errorf("why %q", res.Why)
	}
	if f.head(user) != before {
		t.Error("the user's checkout moved")
	}
	if b, _ := os.ReadFile(filepath.Join(user, "a.txt")); string(b) != "mine\n" {
		t.Errorf("the user's change is gone: %q", b)
	}
	if f.branch(res.Path) != "" || f.head(res.Path) != tip {
		t.Errorf("worktree branch %q head %s, want detached at %s", f.branch(res.Path), f.head(res.Path), tip)
	}
	if len(f.trusted) != 1 || f.trusted[0] != res.Path {
		t.Errorf("trusted %v", f.trusted)
	}
	// Origin moves again: the same worktree is reused and moved forward.
	tip2 := f.advance("main", "c.txt")
	again := f.adopt("", user, user, res.Path)
	if again.Path != res.Path || f.head(res.Path) != tip2 {
		t.Fatalf("reuse = %+v head %s", again, f.head(res.Path))
	}
	// Once the worktree has changes it is left alone and a new one is made.
	if err := os.WriteFile(filepath.Join(res.Path, "a.txt"), []byte("worker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	third := f.adopt("", user, user, res.Path)
	if third.Path == res.Path || filepath.Base(third.Path) != "rc-main-2" {
		t.Fatalf("third = %+v", third)
	}
	if b, _ := os.ReadFile(filepath.Join(res.Path, "a.txt")); string(b) != "worker\n" {
		t.Error("the dirty worktree was touched")
	}
}

// A clone on a feature branch keeps it: the default branch runs in a worktree.
func TestAdoptCloneOnAFeatureBranchUsesAWorktree(t *testing.T) {
	f := newAdoptFix(t)
	user := f.userClone("api")
	f.git(user, "checkout", "-q", "-b", "feature")
	res := f.adopt("main", user, user)
	if res.Path == user || f.branch(user) != "feature" || f.branch(res.Path) != "" {
		t.Fatalf("res = %+v, user on %q, worktree on %q", res, f.branch(user), f.branch(res.Path))
	}
	if !strings.Contains(res.Why, "is on feature, not main") {
		t.Errorf("why %q", res.Why)
	}
}

// A designated branch other than the default gets a worktree tracking
// origin's, which the next request reuses and fast-forwards.
func TestAdoptOtherBranchTracksOriginAndIsReused(t *testing.T) {
	f := newAdoptFix(t)
	f.git(f.work, "checkout", "-q", "-b", "release/1.2")
	f.commit(f.work, "r.txt", "r\n", "release")
	f.git(f.work, "push", "-q", "origin", "release/1.2")
	user := f.userClone("api")
	res := f.adopt("release/1.2", "", user)
	if filepath.Base(res.Path) != "rc-release-1.2" || f.branch(res.Path) != "release/1.2" || f.branch(user) != "main" {
		t.Fatalf("res = %+v on %q", res, f.branch(res.Path))
	}
	if up := f.git(res.Path, "rev-parse", "--abbrev-ref", "release/1.2@{upstream}"); up != "origin/release/1.2" {
		t.Errorf("upstream %q", up)
	}
	tip := f.advance("release/1.2", "s.txt")
	again := f.adopt("release/1.2", "", user, res.Path)
	if again.Path != res.Path || f.head(res.Path) != tip {
		t.Fatalf("again = %+v head %s want %s", again, f.head(res.Path), tip)
	}
	// A branch origin does not have starts from the default branch.
	fresh := f.adopt("belai/fix", "", user)
	if f.branch(fresh.Path) != "belai/fix" || f.head(fresh.Path) != f.git(user, "rev-parse", "refs/remotes/origin/main") {
		t.Errorf("fresh = %+v on %q", fresh, f.branch(fresh.Path))
	}
}

// A local branch with commits that are not on origin is used as it is, never
// reset.
func TestAdoptNeverResetsALocalBranchAhead(t *testing.T) {
	f := newAdoptFix(t)
	f.git(f.work, "checkout", "-q", "-b", "dev")
	f.git(f.work, "push", "-q", "origin", "dev")
	user := f.userClone("api")
	f.git(user, "checkout", "-q", "-b", "dev", "origin/dev")
	mine := f.commit(user, "m.txt", "m\n", "mine")
	f.git(user, "checkout", "-q", "main")
	res := f.adopt("dev", "", user)
	if f.branch(res.Path) != "dev" || f.head(res.Path) != mine {
		t.Fatalf("res = %+v on %q at %s, want dev at %s", res, f.branch(res.Path), f.head(res.Path), mine)
	}
	if f.git(user, "rev-parse", "refs/heads/dev") != mine {
		t.Error("the local branch was moved")
	}
	if !strings.Contains(res.Why, "left as it is") {
		t.Errorf("why %q", res.Why)
	}
}

// A teleport worktree offered before the user's clone does not win: the
// primary checkout is adopted. With only the worktree offered, its repository
// is adopted.
func TestAdoptPrefersThePrimaryCheckoutOverALinkedWorktree(t *testing.T) {
	f := newAdoptFix(t)
	user := f.userClone("api")
	tp := filepath.Join(f.wt, "api-1234", "tp-5678")
	if err := os.MkdirAll(filepath.Dir(tp), 0o755); err != nil {
		t.Fatal(err)
	}
	f.git(user, "worktree", "add", "-q", "--detach", tp, "HEAD")
	tp, _ = Normalize(tp)
	res := f.adopt("", tp, tp, user)
	if res.Path != user || res.Clone != user {
		t.Fatalf("res = %+v", res)
	}
	only := f.adopt("", tp, tp)
	if only.Clone != user || !strings.Contains(only.Why, "an offered worktree belongs to") {
		t.Fatalf("only = %+v", only)
	}
	// Another repository's checkout is never a candidate.
	other := filepath.Join(f.root, "src", "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	f.git(other, "init", "-q")
	f.git(other, "remote", "add", "origin", "https://github.com/acme/other.git")
	r := f.adopt("", other, other)
	if r.Clone == other || !r.Cloned {
		t.Errorf("r = %+v", r)
	}
}

// unreachable makes origin fail every fetch from now on.
func (f *adoptFix) unreachable() {
	f.t.Helper()
	if err := os.Rename(f.bare, f.bare+".gone"); err != nil {
		f.t.Fatal(err)
	}
}

const staleWhy = "could not fetch origin ("

// A fetch failure never refuses a request on a checkout the host has: a clean
// main is still used as it is, on the refs as last fetched.
func TestAdoptFetchFailureCleanMainUsesTheClone(t *testing.T) {
	f := newAdoptFix(t)
	user := f.userClone("api")
	before := f.head(user)
	f.unreachable()
	res, err := f.try("", user, user)
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	if res.Path != user || f.head(user) != before {
		t.Fatalf("res = %+v", res)
	}
	if !strings.Contains(res.Why, staleWhy) || !strings.Contains(res.Why, "); using the last fetched refs; ") {
		t.Errorf("why %q", res.Why)
	}
	t.Logf("why: %s", res.Why)
}

// With origin unreachable a dirty main still gets a detached worktree, at the
// last fetched tip, and the user's change is untouched.
func TestAdoptFetchFailureDirtyMainUsesADetachedWorktree(t *testing.T) {
	f := newAdoptFix(t)
	user := f.userClone("api")
	fetched := f.git(user, "rev-parse", "refs/remotes/origin/main")
	if err := os.WriteFile(filepath.Join(user, "a.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.advance("main", "b.txt") // origin moves, but this host never sees it
	f.unreachable()
	res, err := f.try("", user, user)
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	if filepath.Base(res.Path) != "rc-main" || f.branch(res.Path) != "" || f.head(res.Path) != fetched {
		t.Fatalf("res = %+v head %s want %s", res, f.head(res.Path), fetched)
	}
	if !strings.Contains(res.Why, staleWhy) || !strings.Contains(res.Why, "main is dirty") {
		t.Errorf("why %q", res.Why)
	}
	if b, _ := os.ReadFile(filepath.Join(user, "a.txt")); string(b) != "mine\n" {
		t.Error("the user's change is gone")
	}
}

// A designated branch that was never fetched, while origin is unreachable,
// falls back to the default branch's last fetched tip rather than refusing or
// inventing the branch. One fetched earlier is used as last fetched.
func TestAdoptFetchFailureNeverFetchedBranchFallsBack(t *testing.T) {
	f := newAdoptFix(t)
	f.git(f.work, "checkout", "-q", "-b", "dev")
	devTip := f.commit(f.work, "d.txt", "d\n", "dev")
	f.git(f.work, "push", "-q", "origin", "dev")
	user := f.userClone("api")
	f.unreachable() // the clone has origin/dev and no local dev
	res, err := f.try("feat/never", "", user)
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	if filepath.Base(res.Path) != "rc-main" || f.branch(res.Path) != "" || f.head(res.Path) != f.git(user, "rev-parse", "refs/remotes/origin/main") {
		t.Fatalf("res = %+v on %q", res, f.branch(res.Path))
	}
	if !strings.Contains(res.Why, staleWhy) || !strings.Contains(res.Why, "feat/never is not here and could not be fetched") {
		t.Errorf("why %q", res.Why)
	}
	if f.git(user, "branch", "--list", "feat/never") != "" {
		t.Error("the branch was invented")
	}
	dev, err := f.try("dev", "", user, res.Path)
	if err != nil {
		t.Fatalf("dev refused: %v", err)
	}
	if f.branch(dev.Path) != "dev" || f.head(dev.Path) != devTip || !strings.Contains(dev.Why, staleWhy) {
		t.Fatalf("dev = %+v on %q at %s", dev, f.branch(dev.Path), f.head(dev.Path))
	}
	// Without a checkout and with the clone failing, the request is refused.
	f2 := newAdoptFix(t)
	f2.unreachable()
	if _, err := f2.try("", ""); err == nil || !strings.Contains(err.Error(), "could not clone acme/api") {
		t.Errorf("no checkout, clone fails: %v", err)
	}
}

// makeBase refuses a symbolic link on the way to a worktree.
func TestAdoptRefusesASymlinkedWorktreesBase(t *testing.T) {
	f := newAdoptFix(t)
	user := f.userClone("api")
	if err := os.WriteFile(filepath.Join(user, "a.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.wt, 0o755); err != nil {
		t.Fatal(err)
	}
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(f.wt, config.WorkdirKey(user))); err != nil {
		t.Fatal(err)
	}
	if _, err := f.try("", user, user); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("err = %v", err)
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Error("a worktree was made through the link")
	}
}
