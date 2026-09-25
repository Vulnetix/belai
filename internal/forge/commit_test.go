package forge

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newCommitTestRepo returns a fresh git repository with one committed file,
// using an isolated git config and identity so the host config cannot leak in.
func newCommitTestRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")

	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	git("init", "-q")
	writeFile(t, filepath.Join(dir, "tracked.go"), "package p\n")
	git("add", "-A")
	git("commit", "-q", "-m", "initial")
	return dir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestCommitPathsOnlyListedPaths(t *testing.T) {
	dir := newCommitTestRepo(t)
	writeFile(t, filepath.Join(dir, "keep.txt"), "base\n")
	writeFile(t, filepath.Join(dir, "dirty.txt"), "base\n")
	writeFile(t, filepath.Join(dir, "staged.txt"), "base\n")
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-q", "-m", "base")

	writeFile(t, filepath.Join(dir, "keep.txt"), "changed keep\n")
	writeFile(t, filepath.Join(dir, "dirty.txt"), "changed dirty\n")
	writeFile(t, filepath.Join(dir, "staged.txt"), "changed staged\n")
	gitOut(t, dir, "add", "staged.txt")

	sha, err := CommitPaths(context.Background(), ExecRunner, dir, []string{"keep.txt"}, "fix: keep")
	if err != nil {
		t.Fatalf("CommitPaths: %v", err)
	}
	if sha == "" {
		t.Fatal("CommitPaths returned an empty sha")
	}

	names := gitOut(t, dir, "show", "--name-only", "--format=", "HEAD")
	if names != "keep.txt" {
		t.Fatalf("commit contains %q, want only keep.txt", names)
	}

	unstaged := gitOut(t, dir, "diff", "--name-only")
	staged := gitOut(t, dir, "diff", "--cached", "--name-only")
	if unstaged != "dirty.txt" {
		t.Fatalf("unstaged = %q, want only dirty.txt", unstaged)
	}
	if staged != "staged.txt" {
		t.Fatalf("staged = %q, want only staged.txt untouched", staged)
	}
}

func TestCommitPathsDeletedPath(t *testing.T) {
	dir := newCommitTestRepo(t)
	writeFile(t, filepath.Join(dir, "gone.txt"), "bye\n")
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-q", "-m", "base")
	if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil {
		t.Fatal(err)
	}

	sha, err := CommitPaths(context.Background(), ExecRunner, dir, []string{"gone.txt"}, "chore: remove")
	if err != nil {
		t.Fatalf("CommitPaths: %v", err)
	}
	if sha == "" {
		t.Fatal("CommitPaths returned an empty sha")
	}
	nameStatus := gitOut(t, dir, "show", "--name-status", "--format=", "HEAD")
	if nameStatus != "D\tgone.txt" {
		t.Fatalf("name-status = %q, want a deletion", nameStatus)
	}
}

func TestCommitPathsIgnoresIgnoredPath(t *testing.T) {
	dir := newCommitTestRepo(t)
	writeFile(t, filepath.Join(dir, ".gitignore"), "ignored.txt\n")
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-q", "-m", "gitignore")
	writeFile(t, filepath.Join(dir, "ignored.txt"), "secret\n")

	before := gitOut(t, dir, "rev-parse", "HEAD")
	sha, err := CommitPaths(context.Background(), ExecRunner, dir, []string{"ignored.txt"}, "chore: ignored")
	if err != nil {
		t.Fatalf("CommitPaths: %v", err)
	}
	if sha != "" {
		t.Fatalf("ignored path must not be committed, got %s", sha)
	}
	if after := gitOut(t, dir, "rev-parse", "HEAD"); after != before {
		t.Fatalf("HEAD moved from %s to %s", before, after)
	}
}

func TestCommitPathsEmptyAndNoopPaths(t *testing.T) {
	dir := newCommitTestRepo(t)
	before := gitOut(t, dir, "rev-parse", "HEAD")

	sha, err := CommitPaths(context.Background(), ExecRunner, dir, nil, "chore: empty")
	if err != nil || sha != "" {
		t.Fatalf("empty path set = (%q, %v), want (\"\", nil)", sha, err)
	}

	// A tracked but unchanged path stages nothing, so no commit is made.
	sha, err = CommitPaths(context.Background(), ExecRunner, dir, []string{"tracked.go"}, "chore: noop")
	if err != nil || sha != "" {
		t.Fatalf("no-op path set = (%q, %v), want (\"\", nil)", sha, err)
	}
	if after := gitOut(t, dir, "rev-parse", "HEAD"); after != before {
		t.Fatalf("HEAD moved from %s to %s", before, after)
	}
}

func TestCommitPathsDropsOutsidePaths(t *testing.T) {
	dir := newCommitTestRepo(t)
	writeFile(t, filepath.Join(dir, "in.txt"), "in\n")
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-q", "-m", "in")
	writeFile(t, filepath.Join(dir, "in.txt"), "changed in\n")

	// Absolute paths, escaping paths and duplicates are dropped before staging.
	sha, err := CommitPaths(context.Background(), ExecRunner, dir, []string{
		"/etc/passwd",
		"../outside.txt",
		"in.txt",
		"in.txt",
	}, "chore: in")
	if err != nil {
		t.Fatalf("CommitPaths: %v", err)
	}
	if sha == "" {
		t.Fatal("the valid path should still commit")
	}
	if names := gitOut(t, dir, "show", "--name-only", "--format=", "HEAD"); names != "in.txt" {
		t.Fatalf("commit contains %q, want only in.txt", names)
	}
}

func TestTaskCommitMessageTypes(t *testing.T) {
	cases := []struct {
		objective string
		paths     []string
		wantType  string
	}{
		{"document the export", []string{"README.md"}, "docs"},
		{"cover the hook", []string{"commit_test.go"}, "test"},
		{"wire the workflow", []string{".github/workflows/ci.yml"}, "ci"},
		{"fix the switcher", []string{"app.go"}, "fix"},
		{"refactor the loader", []string{"app.go"}, "refactor"},
	}
	for _, c := range cases {
		msg := TaskCommitMessage(c.objective, c.paths)
		if !strings.HasPrefix(msg, c.wantType+": ") {
			t.Errorf("%q %v: got %q, want type %s", c.objective, c.paths, msg, c.wantType)
		}
	}
}

func TestTaskCommitMessageFeatAndChore(t *testing.T) {
	dir := newCommitTestRepo(t)
	t.Chdir(dir)

	writeFile(t, filepath.Join(dir, "new.go"), "package p\n")
	if msg := TaskCommitMessage("ship the new feature", []string{"new.go"}); !strings.HasPrefix(msg, "feat: ") {
		t.Fatalf("untracked path should be feat, got %q", msg)
	}

	writeFile(t, filepath.Join(dir, "tracked.go"), "package p\n\n// changed\n")
	if msg := TaskCommitMessage("ship the new feature", []string{"tracked.go"}); !strings.HasPrefix(msg, "chore: ") {
		t.Fatalf("tracked modified path should be chore, got %q", msg)
	}
}

func TestTaskCommitMessageHeaderAndBodyLimits(t *testing.T) {
	long := strings.Repeat("x", 200)
	paths := make([]string, 60)
	for i := range paths {
		paths[i] = strings.Repeat("p", 1) + string(rune('a'+i%26)) + ".go"
	}
	msg := TaskCommitMessage(long, paths)
	firstLine := strings.SplitN(msg, "\n", 2)[0]
	if n := len([]rune(firstLine)); n > 72 {
		t.Fatalf("header length = %d, want <= 72", n)
	}
	body := strings.Split(strings.TrimPrefix(msg, firstLine+"\n\n"), "\n")
	if len(body) != 50 {
		t.Fatalf("body lists %d paths, want capped at 50", len(body))
	}
}
