package filediff

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// gitRepo builds a hermetic repository with one committed file.
func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()

	env := append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q")
	write(t, filepath.Join(dir, "tracked.go"), "package main\n\nfunc main() {}\n")
	run("add", ".")
	run("commit", "-qm", "initial")
	return dir
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// observe runs fn between Before and After, the way the agent loop does.
func observe(t *testing.T, r *Recorder, command string, fn func()) Change {
	t.Helper()
	snap := r.Before(context.Background(), command)
	fn()
	return snap.After(context.Background())
}

func pathsOf(c Change) []string {
	var out []string
	for _, f := range c.Files {
		out = append(out, f.Path)
	}
	sort.Strings(out)
	return out
}

func TestGitDetectsModifiedFile(t *testing.T) {
	dir := gitRepo(t)
	r := NewRecorder(dir)

	c := observe(t, r, "whatever", func() {
		write(t, filepath.Join(dir, "tracked.go"), "package main\n\nfunc main() { println(1) }\n")
	})

	if got := pathsOf(c); len(got) != 1 || got[0] != "tracked.go" {
		t.Fatalf("paths = %v (unavailable %q)", got, c.Unavailable)
	}
	if !strings.Contains(c.Files[0].New, "println(1)") {
		t.Fatalf("new side wrong: %q", c.Files[0].New)
	}
	if !strings.Contains(c.Files[0].Old, "func main() {}") {
		t.Fatalf("old side wrong: %q", c.Files[0].Old)
	}
}

// TestGitDetectsSecondEditToAlreadyDirtyFile is the case a set difference of
// status codes cannot see: a file that was already modified stays " M" when it
// is modified again, so only its content settles it.
func TestGitDetectsSecondEditToAlreadyDirtyFile(t *testing.T) {
	dir := gitRepo(t)
	r := NewRecorder(dir)
	write(t, filepath.Join(dir, "tracked.go"), "package main\n\nfunc main() { println(1) }\n")

	c := observe(t, r, "whatever", func() {
		write(t, filepath.Join(dir, "tracked.go"), "package main\n\nfunc main() { println(2) }\n")
	})

	if got := pathsOf(c); len(got) != 1 || got[0] != "tracked.go" {
		t.Fatalf("second edit to a dirty file went unnoticed: %v (%q)", got, c.Unavailable)
	}
	if !strings.Contains(c.Files[0].Old, "println(1)") {
		t.Fatalf("old side should be the pre-command content, got %q", c.Files[0].Old)
	}
}

func TestGitDetectsCreatedFile(t *testing.T) {
	dir := gitRepo(t)
	r := NewRecorder(dir)

	c := observe(t, r, "whatever", func() {
		write(t, filepath.Join(dir, "fresh.go"), "package main\n")
	})

	if got := pathsOf(c); len(got) != 1 || got[0] != "fresh.go" {
		t.Fatalf("paths = %v", got)
	}
	if !c.Files[0].Created || c.Files[0].Old != "" {
		t.Fatalf("expected a creation, got %+v", c.Files[0])
	}
}

func TestGitDetectsDeletedFile(t *testing.T) {
	dir := gitRepo(t)
	r := NewRecorder(dir)

	c := observe(t, r, "whatever", func() {
		os.Remove(filepath.Join(dir, "tracked.go"))
	})

	if got := pathsOf(c); len(got) != 1 || got[0] != "tracked.go" {
		t.Fatalf("paths = %v", got)
	}
	if !c.Files[0].Deleted {
		t.Fatalf("expected a deletion, got %+v", c.Files[0])
	}
}

// TestGitIgnoresStagingWithoutContentChange: `git add` moves a file's status
// but changes nothing on disk, and a diff of nothing is noise.
func TestGitIgnoresStagingWithoutContentChange(t *testing.T) {
	dir := gitRepo(t)
	r := NewRecorder(dir)
	write(t, filepath.Join(dir, "tracked.go"), "package main\n\nfunc main() { println(1) }\n")

	c := observe(t, r, "git add .", func() {
		cmd := exec.Command("git", "-C", dir, "add", ".")
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git add: %v\n%s", err, out)
		}
	})

	if len(c.Files) != 0 {
		t.Fatalf("staging produced a diff: %v", pathsOf(c))
	}
}

// TestGitDiscoversChangesItCouldNotHaveParsed is the whole reason the git path
// is authoritative: the command is opaque, and the change is still found.
func TestGitDiscoversChangesItCouldNotHaveParsed(t *testing.T) {
	dir := gitRepo(t)
	r := NewRecorder(dir)

	if _, confident := ParseTargets("make build"); confident {
		t.Fatal("precondition: the parser should not understand this command")
	}
	c := observe(t, r, "make build", func() {
		write(t, filepath.Join(dir, "generated.go"), "package main\n")
	})
	if got := pathsOf(c); len(got) != 1 || got[0] != "generated.go" {
		t.Fatalf("paths = %v", got)
	}
}

// TestGitWorkdirBelowRepoRoot: status paths are repo-root-relative whatever
// -C is, so a workdir in a subdirectory has to be rebased for display.
func TestGitWorkdirBelowRepoRoot(t *testing.T) {
	root := gitRepo(t)
	sub := filepath.Join(root, "internal", "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	r := NewRecorder(sub)
	if r.RepoRoot != root {
		t.Fatalf("repo root = %q, want %q", r.RepoRoot, root)
	}

	c := observe(t, r, "whatever", func() {
		write(t, filepath.Join(sub, "local.go"), "package pkg\n")
	})
	if got := pathsOf(c); len(got) != 1 || got[0] != "local.go" {
		t.Fatalf("paths = %v, want the path relative to the workdir", got)
	}
}

func TestGitMarksBinaryFiles(t *testing.T) {
	dir := gitRepo(t)
	r := NewRecorder(dir)

	c := observe(t, r, "whatever", func() {
		if err := os.WriteFile(filepath.Join(dir, "blob.bin"), []byte{1, 2, 0, 3}, 0o644); err != nil {
			t.Fatal(err)
		}
	})
	if len(c.Files) != 1 || !c.Files[0].Binary {
		t.Fatalf("expected a binary file, got %+v", c.Files)
	}
	if rows := c.Files[0].Rows(); rows != nil {
		t.Fatal("binary files must not produce rows")
	}
}

func TestGitCapsFileCount(t *testing.T) {
	dir := gitRepo(t)
	r := NewRecorder(dir)
	r.MaxFiles = 2

	c := observe(t, r, "whatever", func() {
		for _, n := range []string{"a.go", "b.go", "c.go"} {
			write(t, filepath.Join(dir, n), "package main\n")
		}
	})
	if c.Unavailable == "" {
		t.Fatalf("expected an unavailable notice, got %v", pathsOf(c))
	}
	if !strings.Contains(c.Unavailable, "too many") {
		t.Fatalf("unavailable = %q", c.Unavailable)
	}
}

func TestNoChangeProducesNothing(t *testing.T) {
	dir := gitRepo(t)
	r := NewRecorder(dir)
	c := observe(t, r, "ls", func() {})
	if !c.Empty() {
		t.Fatalf("expected an empty change, got %+v", c)
	}
}

// ---- heuristic path -------------------------------------------------------

func TestHeuristicDetectsRedirect(t *testing.T) {
	dir := t.TempDir() // no .git
	r := NewRecorder(dir)
	if r.RepoRoot != "" {
		t.Skip("temp dir is inside a git repository")
	}
	write(t, filepath.Join(dir, "out.txt"), "before\n")

	c := observe(t, r, "echo after > out.txt", func() {
		write(t, filepath.Join(dir, "out.txt"), "after\n")
	})
	if got := pathsOf(c); len(got) != 1 || got[0] != "out.txt" {
		t.Fatalf("paths = %v (%q)", got, c.Unavailable)
	}
	if c.Files[0].Old != "before\n" || c.Files[0].New != "after\n" {
		t.Fatalf("sides wrong: %+v", c.Files[0])
	}
}

func TestHeuristicReportsUnknownCommands(t *testing.T) {
	dir := t.TempDir()
	r := NewRecorder(dir)
	if r.RepoRoot != "" {
		t.Skip("temp dir is inside a git repository")
	}

	c := observe(t, r, "make build", func() {
		write(t, filepath.Join(dir, "surprise.txt"), "x\n")
	})
	if c.Unavailable == "" {
		t.Fatal("an unparseable command outside a repo must say so, not report no changes")
	}
	if len(c.Files) != 0 {
		t.Fatalf("expected no files, got %v", pathsOf(c))
	}
}

func TestHeuristicIgnoresPathsOutsideTheWorkdir(t *testing.T) {
	dir := t.TempDir()
	r := NewRecorder(dir)
	if r.RepoRoot != "" {
		t.Skip("temp dir is inside a git repository")
	}
	outside := filepath.Join(t.TempDir(), "elsewhere.txt")
	write(t, outside, "x\n")

	c := observe(t, r, "echo y > "+outside, func() {
		write(t, outside, "y\n")
	})
	for _, f := range c.Files {
		if strings.Contains(f.Path, "elsewhere") {
			t.Fatalf("a path outside the workdir was reported: %+v", f)
		}
	}
}

func TestNilSnapshotIsSafe(t *testing.T) {
	var s *Snapshot
	if c := s.After(context.Background()); !c.Empty() {
		t.Fatalf("nil snapshot should yield an empty change, got %+v", c)
	}
	var r *Recorder
	if snap := r.Before(context.Background(), "x"); snap != nil {
		t.Fatal("nil recorder should yield a nil snapshot")
	}
}
