package review

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/forge"
)

// gitIn runs git in dir for test setup, with no user or system configuration.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, rel, text string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newRepo makes a repository on main with one commit holding a.go and b.go.
func newRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "a.go", "package a\n\nfunc A() int {\n\treturn 1\n}\n")
	write(t, dir, "b.go", "package a\n\nfunc B() int {\n\treturn 2\n}\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "--no-gpg-sign", "-q", "-m", "base")
	return dir
}

// fakeForge is a Provider that serves one scripted pull request.
type fakeForge struct {
	kind       string
	pr         *forge.PR
	diff       forge.PRDiff
	diffErr    error
	diffCalls  int
	comments   []string
	commentErr error
}

func (f *fakeForge) Kind() string  { return f.kind }
func (f *fakeForge) Noun() string  { return "PR" }
func (f *fakeForge) Label() string { return "Fake" }
func (f *fakeForge) PRForBranch(context.Context, string, string) (*forge.PR, error) {
	return f.pr, nil
}
func (f *fakeForge) CreatePR(context.Context, string, forge.CreatePRArgs) (string, error) {
	return "", nil
}
func (f *fakeForge) Checks(context.Context, string, string, forge.PR) ([]forge.Check, error) {
	return nil, nil
}
func (f *fakeForge) OpenPRsWithPrefix(context.Context, string, string) ([]forge.PR, error) {
	return nil, nil
}
func (f *fakeForge) ClosePR(context.Context, string, int, string) error { return nil }
func (f *fakeForge) CommentPR(_ context.Context, _ string, _ int, body string) error {
	if f.commentErr != nil {
		return f.commentErr
	}
	f.comments = append(f.comments, body)
	return nil
}
func (f *fakeForge) DiffPR(context.Context, string, int) (forge.PRDiff, error) {
	f.diffCalls++
	return f.diff, f.diffErr
}

// fakeReviewer answers with a fixed reply and records the prompt.
type fakeReviewer struct {
	reply  string
	err    error
	prompt string
	calls  int
}

func (f *fakeReviewer) Review(_ context.Context, prompt string) (string, error) {
	f.calls++
	f.prompt = prompt
	return f.reply, f.err
}
