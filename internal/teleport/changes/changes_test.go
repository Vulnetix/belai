package changes

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/forge"
)

// These tests run real git in temporary repositories: the origin is a clone of a
// bare repository, the target a second clone, as two hosts would be.

func gitOrSkip(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

type pair struct{ bare, origin, target string }

// newPair makes a bare repository with one pushed commit and two clones of it.
func newPair(t *testing.T) pair {
	t.Helper()
	gitOrSkip(t)
	root := t.TempDir()
	p := pair{bare: filepath.Join(root, "bare.git"), origin: filepath.Join(root, "origin"), target: filepath.Join(root, "target")}
	git(t, root, "init", "--bare", "-b", "main", p.bare)
	git(t, root, "clone", p.bare, p.origin)
	write(t, p.origin, "main.go", "package main\n\nfunc main() {}\n")
	write(t, p.origin, "README.md", "# demo\n")
	write(t, p.origin, "docs/guide.md", "guide\n")
	git(t, p.origin, "add", "-A")
	git(t, p.origin, "commit", "-m", "init")
	git(t, p.origin, "push", "origin", "HEAD:main")
	git(t, p.origin, "branch", "--set-upstream-to=origin/main")
	git(t, root, "clone", p.bare, p.target)
	return p
}

func TestSafePath(t *testing.T) {
	for _, ok := range []string{"a", "a/b.go", "internal/x_y-z/File.test.go", ".github/workflows/ci.yml", "a@b+c/d"} {
		if !SafePath(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "/etc/passwd", "../x", "a/../b", "a//b", "./a", "a/", ".git/config", "a/.git/hooks/pre-commit", ".GIT/x", ".vulnetix/belai/x",
		"a b", "a\"b", "a\nb", "é.go", "a\\b", "-rf", strings.Repeat("a", 401), strings.Repeat("a", 256)} {
		if bad == "-rf" {
			if !SafePath(bad) {
				t.Errorf("%q: a leading dash is fine for a path after --", bad)
			}
			continue
		}
		if SafePath(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestCollectReadsUnpushedAndUncommittedChangesAndLeavesTheCheckoutAlone(t *testing.T) {
	p := newPair(t)
	// An unpushed commit, a tracked edit, a deletion, new files, and things that must not travel.
	write(t, p.origin, "feature.go", "package main\n\nfunc feature() {}\n")
	git(t, p.origin, "add", "-A")
	git(t, p.origin, "commit", "-m", "feature")
	write(t, p.origin, "main.go", "package main\n\nfunc main() { feature() }\n")
	if err := os.Remove(filepath.Join(p.origin, "README.md")); err != nil {
		t.Fatal(err)
	}
	write(t, p.origin, "docs/new.md", "new\n")
	write(t, p.origin, ".env", "SECRET=1\n")
	write(t, p.origin, "creds.pem", "-----BEGIN-----\n")
	write(t, p.origin, "blob.bin", "a\x00b")
	write(t, p.origin, "big.txt", strings.Repeat("x", MaxFileBytes+1))
	write(t, p.origin, "with space.txt", "x\n")
	write(t, p.origin, ".gitignore", "ignored.txt\n")
	write(t, p.origin, "ignored.txt", "never\n")
	if err := os.Symlink("main.go", filepath.Join(p.origin, "link.go")); err != nil {
		t.Fatal(err)
	}
	statusBefore := git(t, p.origin, "status", "--porcelain=v1", "--untracked-files=all")
	headBefore := git(t, p.origin, "rev-parse", "HEAD")

	s, err := Collect(context.Background(), nil, p.origin)
	if err != nil {
		t.Fatal(err)
	}

	if s.Base != git(t, p.origin, "rev-parse", "origin/main") || s.Head != headBefore || s.Commits != 1 || !s.Dirty || s.Branch != "main" {
		t.Fatalf("snapshot = %+v", s)
	}
	var paths []string
	for _, f := range s.Files {
		paths = append(paths, f.Path)
	}
	slices.Sort(paths)
	want := []string{".gitignore", "README.md", "docs/new.md", "feature.go", "main.go"}
	if !slices.Equal(paths, want) {
		t.Fatalf("files = %v, want %v", paths, want)
	}
	status := map[string]string{}
	for _, f := range s.Files {
		status[f.Path] = f.Status
	}
	if status["README.md"] != "deleted" || status["feature.go"] != "added" || status["main.go"] != "modified" {
		t.Fatalf("statuses = %v", status)
	}
	reasons := map[string]string{}
	for _, k := range s.Skipped {
		reasons[k.Path] = k.Reason
	}
	for path, why := range map[string]string{".env": SkipSensitive, "creds.pem": SkipSensitive, "blob.bin": SkipBinary, "big.txt": SkipLarge, "with space.txt": SkipPath, "link.go": SkipSymlink} {
		if reasons[path] != why {
			t.Errorf("%s skipped as %q, want %q (all: %v)", path, reasons[path], why, reasons)
		}
	}
	if _, ok := reasons["ignored.txt"]; ok {
		t.Error("an ignored file is not a candidate at all, so it is not reported either")
	}
	if strings.Contains(s.Patch, "SECRET") || strings.Contains(s.Patch, "BEGIN") || strings.Contains(s.Patch, "never") {
		t.Fatal("a credential or ignored file reached the patch")
	}
	// The origin's checkout, index and HEAD are exactly as they were.
	if git(t, p.origin, "status", "--porcelain=v1", "--untracked-files=all") != statusBefore || git(t, p.origin, "rev-parse", "HEAD") != headBefore {
		t.Fatal("Collect changed the origin's checkout")
	}
	if _, err := ParsePatch(s.Patch); err != nil {
		t.Fatalf("the patch Collect wrote does not parse: %v", err)
	}
}

func TestACleanCheckoutHasNothingToMove(t *testing.T) {
	p := newPair(t)

	if _, err := Collect(context.Background(), nil, p.origin); !errors.Is(err, ErrNothing) {
		t.Fatalf("clean and pushed: %v, want ErrNothing", err)
	}
	// Only a skipped file changed: still nothing to send.
	write(t, p.origin, ".env", "X=1\n")
	if _, err := Collect(context.Background(), nil, p.origin); !errors.Is(err, ErrNothing) {
		t.Fatalf("only a credential file: %v, want ErrNothing", err)
	}
}

func TestARepositoryWithNoPushedBaseCannotBeReplayed(t *testing.T) {
	gitOrSkip(t)
	dir := t.TempDir()
	git(t, dir, "init", "-b", "main")
	write(t, dir, "a.txt", "a\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-m", "x")

	if _, err := Collect(context.Background(), nil, dir); !errors.Is(err, ErrNoBase) {
		t.Fatalf("no remote: %v, want ErrNoBase", err)
	}
}

func TestChangesApplyOnAnotherCloneAndTheTreesMatch(t *testing.T) {
	p := newPair(t)
	write(t, p.origin, "feature.go", "package main\n\nfunc feature() {}\n")
	git(t, p.origin, "add", "-A")
	git(t, p.origin, "commit", "-m", "feature")
	write(t, p.origin, "main.go", "package main\n\nfunc main() { feature() }\n")
	if err := os.Remove(filepath.Join(p.origin, "README.md")); err != nil {
		t.Fatal(err)
	}
	write(t, p.origin, "docs/new.md", "new\n")
	write(t, p.origin, "no-newline.txt", "last line without a newline")
	s, err := Collect(context.Background(), nil, p.origin)
	if err != nil {
		t.Fatal(err)
	}

	// The target is at the base commit, as a worktree at it would be.
	if got := git(t, p.target, "rev-parse", "HEAD"); got != s.Base {
		t.Fatalf("target at %s, base %s", got, s.Base)
	}
	secs, err := ParsePatch(s.Patch)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Apply(context.Background(), nil, p.target, secs)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Failed) != 0 || len(res.Applied) != len(secs) {
		t.Fatalf("result = %+v", res)
	}
	ok, tree, err := Matches(context.Background(), nil, p.target, s.Tree)
	if err != nil || !ok {
		t.Fatalf("trees differ: ok=%v tree=%s want=%s err=%v", ok, tree, s.Tree, err)
	}
	if b, _ := os.ReadFile(filepath.Join(p.target, "main.go")); !strings.Contains(string(b), "feature()") {
		t.Fatalf("main.go = %q", b)
	}
	if _, err := os.Stat(filepath.Join(p.target, "README.md")); !os.IsNotExist(err) {
		t.Fatal("README.md was not deleted")
	}
	changed, err := Changed(context.Background(), nil, p.target)
	if err != nil || len(changed) != 5 {
		t.Fatalf("changed = %v err=%v", changed, err)
	}
}

func TestAFileThatDoesNotApplyIsReportedWithItsPartAndTheRestStillApply(t *testing.T) {
	p := newPair(t)
	write(t, p.origin, "main.go", "package main\n\nfunc main() { origin() }\n")
	write(t, p.origin, "docs/guide.md", "guide, edited on the origin\n")
	s, err := Collect(context.Background(), nil, p.origin)
	if err != nil {
		t.Fatal(err)
	}
	// The target's copy of main.go has moved on, so that part cannot apply.
	write(t, p.target, "main.go", "package main\n\nfunc main() { target() }\n")

	secs, _ := ParsePatch(s.Patch)
	res, err := Apply(context.Background(), nil, p.target, secs)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Applied, []string{"docs/guide.md"}) || len(res.Failed) != 1 {
		t.Fatalf("result = %+v", res)
	}
	f := res.Failed[0]
	if f.Path != "main.go" || f.Status != "modified" || !strings.Contains(f.Section, "origin()") || f.Reason == "" {
		t.Fatalf("failure = %+v", f)
	}
	if strings.Contains(f.Reason, "belai-teleport-patch") {
		t.Fatalf("the reason names a temporary file: %q", f.Reason)
	}
	if b, _ := os.ReadFile(filepath.Join(p.target, "main.go")); !strings.Contains(string(b), "target()") {
		t.Fatal("a part that did not apply changed the file")
	}
	if ok, _, _ := Matches(context.Background(), nil, p.target, s.Tree); ok {
		t.Fatal("the trees match although a file was not applied")
	}
}

func TestApplyRefusesADirectoryThatIsNotAWorktreeRoot(t *testing.T) {
	p := newPair(t)
	if err := os.MkdirAll(filepath.Join(p.target, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	secs := []Section{{Path: "x.txt", Status: "added", Text: "diff --git a/x.txt b/x.txt\nnew file mode 100644\nindex " + strings.Repeat("0", 40) + ".." + strings.Repeat("a", 40) + "\n--- /dev/null\n+++ b/x.txt\n@@ -0,0 +1 @@\n+x\n"}}

	if _, err := Apply(context.Background(), nil, filepath.Join(p.target, "docs"), secs); err == nil {
		t.Fatal("a subdirectory was accepted as the root")
	}
	if _, err := os.Stat(filepath.Join(p.target, "x.txt")); err == nil {
		t.Fatal("a file was written")
	}
}

func TestApplyNeverWritesThroughASymlink(t *testing.T) {
	p := newPair(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(p.target, "docs", "out")); err != nil {
		t.Fatal(err)
	}
	secs := []Section{{Path: "docs/out/pwned.txt", Status: "added", Text: "diff --git a/docs/out/pwned.txt b/docs/out/pwned.txt\nnew file mode 100644\nindex " + strings.Repeat("0", 40) + ".." + strings.Repeat("a", 40) + "\n--- /dev/null\n+++ b/docs/out/pwned.txt\n@@ -0,0 +1 @@\n+pwned\n"}}

	res, err := Apply(context.Background(), nil, p.target, secs)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Failed) != 1 || len(res.Applied) != 0 {
		t.Fatalf("result = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(outside, "pwned.txt")); err == nil {
		t.Fatal("a file was written through a symlink")
	}
}

func TestMatchesRefusesAnythingButATreeID(t *testing.T) {
	p := newPair(t)

	for _, bad := range []string{"", "HEAD", "--oops", "abc"} {
		if _, _, err := Matches(context.Background(), nil, p.target, bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// allowFile is the hardened runner with the file transport allowed, because the
// tests' "forge" is a bare repository on disk.
func allowFile(env ...string) forge.Runner {
	inner := forge.HardenedGit("", "", env...)
	return func(ctx context.Context, dir string, argv ...string) ([]byte, error) {
		if len(argv) > 0 && argv[0] == "git" {
			argv = append([]string{"git", "-c", "protocol.file.allow=always"}, argv[1:]...)
		}
		return inner(ctx, dir, argv...)
	}
}

// onForge makes both clones' origin look like a repository on GitHub whose address
// git rewrites to the bare repository on disk, so Push sees a forge remote.
func onForge(t *testing.T, p pair) {
	t.Helper()
	const addr = "https://github.com/acme/demo.git"
	for _, dir := range []string{p.origin, p.target} {
		git(t, dir, "remote", "set-url", "origin", addr)
		git(t, dir, "config", "url."+p.bare+".insteadOf", addr)
	}
}

func TestPushPutsOneBranchOnTheForgeHoldingTheFinalState(t *testing.T) {
	p := newPair(t)
	onForge(t, p)
	write(t, p.origin, "feature.go", "package main\n")
	git(t, p.origin, "add", "-A")
	git(t, p.origin, "commit", "-m", "feature")
	write(t, p.origin, "main.go", "package main\n\nfunc main() { feature() }\n")
	s, err := Collect(context.Background(), allowFile, p.origin)
	if err != nil {
		t.Fatal(err)
	}
	headBefore := git(t, p.origin, "rev-parse", "HEAD")
	statusBefore := git(t, p.origin, "status", "--porcelain=v1")

	got, err := Push(context.Background(), allowFile, p.origin, s, "3f2a9c10-6b7e-4c1d-8a52-0d9e4f1b7a33")
	if err != nil {
		t.Fatal(err)
	}

	if got.Branch != "belai/teleport/3f2a9c10" {
		t.Fatalf("branch = %q", got.Branch)
	}
	// The forge has exactly the new branch, at a commit whose tree is the final state and parent the origin's HEAD.
	if git(t, p.bare, "rev-parse", "refs/heads/belai/teleport/3f2a9c10") != got.Commit {
		t.Fatal("the branch is not at the commit")
	}
	if git(t, p.bare, "rev-parse", got.Commit+"^{tree}") != s.Tree || git(t, p.bare, "rev-parse", got.Commit+"^") != s.Head {
		t.Fatal("the commit is not the final tree on the origin's head")
	}
	if git(t, p.bare, "for-each-ref", "--format=%(refname)", "refs/heads/") != "refs/heads/belai/teleport/3f2a9c10\nrefs/heads/main" {
		t.Fatalf("the forge's branches changed beyond the teleport branch: %s", git(t, p.bare, "for-each-ref"))
	}
	if git(t, p.origin, "rev-parse", "HEAD") != headBefore || git(t, p.origin, "status", "--porcelain=v1") != statusBefore {
		t.Fatal("the push changed the checkout")
	}
	if a := git(t, p.bare, "log", "-1", "--format=%an <%ae>", got.Commit); !strings.HasPrefix(a, "Belai teleport") {
		t.Fatalf("author = %q", a)
	}

	// A target that fetches the branch has the final state at that commit.
	git(t, p.target, "-c", "protocol.file.allow=always", "fetch", "origin", got.Branch)
	if git(t, p.target, "rev-parse", "FETCH_HEAD^{tree}") != s.Tree {
		t.Fatal("the fetched commit's tree is not the final state")
	}
}

func TestPushRefusesWhatItShould(t *testing.T) {
	p := newPair(t)
	onForge(t, p)
	write(t, p.origin, "main.go", "package main\n\nfunc main() { x() }\n")
	s, err := Collect(context.Background(), allowFile, p.origin)
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"", "zzzzzzzz", "--force", "../../x"} {
		if _, err := Push(context.Background(), allowFile, p.origin, s, id); err == nil {
			t.Errorf("teleport id %q accepted", id)
		}
	}
	if _, err := Push(context.Background(), allowFile, p.origin, Snapshot{}, "3f2a9c10-aaaa"); err == nil {
		t.Error("an empty snapshot was pushed")
	}
	// The hardened default refuses the file transport, so a push to a path remote fails and is reported.
	if _, err := Push(context.Background(), nil, p.origin, s, "3f2a9c10-aaaa"); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Errorf("hardened push to a path remote: %v", err)
	}
	// No origin remote on a forge.
	lone := t.TempDir()
	git(t, lone, "init", "-b", "main")
	write(t, lone, "a", "a\n")
	git(t, lone, "add", "-A")
	git(t, lone, "commit", "-m", "x")
	ls := Snapshot{Head: git(t, lone, "rev-parse", "HEAD"), Tree: git(t, lone, "rev-parse", "HEAD^{tree}")}
	if _, err := Push(context.Background(), allowFile, lone, ls, "3f2a9c10-aaaa"); !errors.Is(err, ErrNoRemote) {
		t.Errorf("no remote: %v, want ErrNoRemote", err)
	}
}

func TestCheckComparesEachFileWithTheOriginsObjectIds(t *testing.T) {
	p := newPair(t)
	write(t, p.origin, "main.go", "package main\n\nfunc main() { origin() }\n")
	write(t, p.origin, "docs/new.md", "new\n")
	if err := os.Remove(filepath.Join(p.origin, "README.md")); err != nil {
		t.Fatal(err)
	}
	s, err := Collect(context.Background(), nil, p.origin)
	if err != nil {
		t.Fatal(err)
	}
	secs, _ := ParsePatch(s.Patch)
	for _, sec := range secs {
		if sec.Status == "deleted" && sec.Blob != "" {
			t.Fatalf("a deletion has no blob: %+v", sec)
		}
		if sec.Status != "deleted" && sec.Blob == "" {
			t.Fatalf("%s has no blob", sec.Path)
		}
	}

	// Nothing applied: only the file nobody touched matches nothing, and the deletion is not done.
	before := map[string]bool{}
	for _, c := range Check(context.Background(), nil, p.target, secs) {
		before[c.Path] = c.Match
	}
	if before["main.go"] || before["docs/new.md"] || before["README.md"] {
		t.Fatalf("nothing applied yet, but %v match", before)
	}

	res, err := Apply(context.Background(), nil, p.target, secs)
	if err != nil || len(res.Failed) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	for _, c := range Check(context.Background(), nil, p.target, secs) {
		if !c.Match {
			t.Errorf("%s does not match after a clean apply", c.Path)
		}
	}

	// A file edited by hand to something else no longer matches.
	write(t, p.target, "main.go", "package main\n\nfunc main() { different() }\n")
	for _, c := range Check(context.Background(), nil, p.target, secs) {
		if c.Path == "main.go" && c.Match {
			t.Error("a changed file still matches")
		}
	}
}
