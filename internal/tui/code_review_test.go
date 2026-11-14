package tui

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/forge"
	"github.com/vulnetix/belai/internal/review"
)

func TestParseReviewRequest(t *testing.T) {
	cases := []struct {
		in    string
		want  reviewRequest
		fails bool
	}{
		{"", reviewRequest{}, false},
		{"pr", reviewRequest{target: review.Target{Kind: review.KindPR}}, false},
		{"pr 12", reviewRequest{target: review.Target{Kind: review.KindPR, Ref: "12"}}, false},
		{"pr #12 force", reviewRequest{target: review.Target{Kind: review.KindPR, Ref: "#12"}, force: true}, false},
		{"pr current", reviewRequest{target: review.Target{Kind: review.KindPR, Ref: "current"}}, false},
		{"branch", reviewRequest{target: review.Target{Kind: review.KindBranch}}, false},
		{"branch feat/x --base main", reviewRequest{target: review.Target{Kind: review.KindBranch, Ref: "feat/x", Base: "main"}}, false},
		{"--base origin/main", reviewRequest{target: review.Target{Kind: review.KindBranch, Base: "origin/main"}}, false},
		{"branch force", reviewRequest{target: review.Target{Kind: review.KindBranch}, force: true}, false},
		{"post", reviewRequest{post: true}, false},
		{"--base", reviewRequest{}, true},
		{"pr 3 --base main", reviewRequest{}, true},
		{"post pr 3", reviewRequest{}, true},
		{"post force", reviewRequest{}, true},
		{"everything", reviewRequest{}, true},
	}
	for _, c := range cases {
		got, why := parseReviewRequest(c.in)
		if (why != "") != c.fails || (!c.fails && got != c.want) {
			t.Errorf("%q: %+v %q", c.in, got, why)
		}
	}
}

type tuiReviewer struct {
	reply string
	calls int
}

func (r *tuiReviewer) Review(context.Context, string) (string, error) {
	r.calls++
	return r.reply, nil
}

type tuiForge struct {
	forge.Provider
	comments []string
	err      error
}

func (f *tuiForge) Kind() string  { return forge.KindGitHub }
func (f *tuiForge) Noun() string  { return "PR" }
func (f *tuiForge) Label() string { return "fake" }
func (f *tuiForge) PRForBranch(context.Context, string, string) (*forge.PR, error) {
	return &forge.PR{Number: 4}, nil
}
func (f *tuiForge) DiffPR(context.Context, string, int) (forge.PRDiff, error) {
	return forge.PRDiff{
		Diff:    "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1,2 @@\n package a\n+var x = 1\n",
		HeadSHA: strings.Repeat("ef", 20),
	}, nil
}
func (f *tuiForge) CommentPR(_ context.Context, _ string, _ int, body string) error {
	if f.err != nil {
		return f.err
	}
	f.comments = append(f.comments, body)
	return nil
}

func codeReviewApp(t *testing.T, s config.Settings, reply string) (*App, *tuiReviewer, *tuiForge) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	dir := t.TempDir()
	env := append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x.test", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x.test")
	if err := os.WriteFile(dir+"/a.go", []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "."}, {"commit", "--no-gpg-sign", "-q", "-m", "base"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.WriteFile(dir+"/a.go", []byte("package a\nvar x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := New(Options{Provider: "openai", Model: "gpt-5", Workdir: dir})
	a.settings = s
	rv, f := &tuiReviewer{reply: reply}, &tuiForge{}
	a.codeReview.reviewer = rv
	a.codeReview.provider = func() (forge.Provider, error) { return f, nil }
	a.codeReview.store = &review.Store{Dir: t.TempDir()}
	return a, rv, f
}

func runCodeReview(t *testing.T, a *App, arg string) {
	t.Helper()
	cmd := a.handleCommand("/review " + arg)
	if cmd == nil {
		t.Fatalf("/review %q started nothing; last row: %q", arg, lastSystemMessage(a))
	}
	msg, ok := cmd().(codeReviewDoneMsg)
	if !ok {
		t.Fatalf("unexpected message %T", cmd())
	}
	a.Update(msg)
}

func TestReviewShowsTheResultAndLeavesTheConversationAlone(t *testing.T) {
	a, rv, _ := codeReviewApp(t, config.Settings{}, "verdict: request_changes\nfinding: a.go:2: major correctness: x is unused\nfix: use it\n")
	beforeTurns, beforeFile := len(a.buildTurns()), string(sessionFileBytes(t, a))
	runCodeReview(t, a, "")
	row := lastSystemMessage(a)
	for _, want := range []string{"verdict: request_changes (advisory)", "1. a.go:2 major/correctness: x is unused", "fix: use it"} {
		if !strings.Contains(row, want) {
			t.Errorf("row lacks %q:\n%s", want, row)
		}
	}
	if a.codeReview.busy || a.codeReview.last == nil || rv.calls != 1 {
		t.Fatalf("state: busy=%v last=%v calls=%d", a.codeReview.busy, a.codeReview.last, rv.calls)
	}
	last := a.messages[len(a.messages)-1]
	if !last.Ephemeral {
		t.Error("the result row is part of the record")
	}
	if len(a.buildTurns()) != beforeTurns || string(sessionFileBytes(t, a)) != beforeFile {
		t.Error("the review changed the conversation or the session file")
	}
	// A working tree review is not offered for posting.
	if strings.Contains(row, "/review post") {
		t.Error("a working tree review offered posting")
	}
}

func TestReviewRefusals(t *testing.T) {
	off := false
	a, rv, _ := codeReviewApp(t, config.Settings{Review: &config.ReviewSettings{Enabled: &off}}, "verdict: approve")
	if cmd := a.handleCommand("/review"); cmd != nil || !strings.Contains(lastSystemMessage(a), "review.enabled is false") {
		t.Fatalf("disabled: %q", lastSystemMessage(a))
	}
	a, rv, _ = codeReviewApp(t, config.Settings{}, "verdict: approve")
	a.codeReview.busy = true
	if cmd := a.handleCommand("/review"); cmd != nil || !strings.Contains(lastSystemMessage(a), "already running") {
		t.Fatalf("busy: %q", lastSystemMessage(a))
	}
	a.codeReview.busy = false
	if cmd := a.handleCommand("/review bogus"); cmd != nil || !strings.Contains(lastSystemMessage(a), "usage: /review") {
		t.Fatalf("usage: %q", lastSystemMessage(a))
	}
	if rv.calls != 0 {
		t.Error("a refused review called the model")
	}
}

func answerKey(a *App, k string) {
	_, _ = a.handleCodeReviewPostKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
}

func finishPost(t *testing.T, a *App, k string) {
	t.Helper()
	_, cmd := a.handleCodeReviewPostKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
	if cmd == nil {
		t.Fatalf("%q started nothing", k)
	}
	a.Update(cmd())
}

func TestReviewPostNeedsAYes(t *testing.T) {
	reply := "verdict: comment\nfinding: a.go:2: minor docs: add a comment\n"
	a, _, f := codeReviewApp(t, config.Settings{}, reply)
	runCodeReview(t, a, "pr 4")
	if !strings.Contains(lastSystemMessage(a), "/review post") {
		t.Fatalf("posting was not offered: %q", lastSystemMessage(a))
	}
	if len(f.comments) != 0 {
		t.Fatal("a review posted by itself")
	}
	a.handleCommand("/review post")
	if a.view != viewReviewPost {
		t.Fatalf("view = %v, want the post question", a.view)
	}
	if !strings.Contains(a.codeReviewPostView(), "pull request #4") || !strings.Contains(a.codeReviewPostView(), review.Marker(strings.Repeat("ef", 20))) {
		t.Errorf("the question does not show the comment:\n%s", a.codeReviewPostView())
	}
	// esc, n and q deny.
	for _, k := range []string{"n", "q"} {
		answerKey(a, k)
		if len(f.comments) != 0 || a.view != viewChat {
			t.Fatalf("%q: comments %d view %v", k, len(f.comments), a.view)
		}
		a.handleCommand("/review post")
	}
	finishPost(t, a, "y")
	if len(f.comments) != 1 || !strings.Contains(lastSystemMessage(a), "posted to pull request #4") || a.view != viewChat {
		t.Fatalf("after y: %d comments, last row %q, view %v", len(f.comments), lastSystemMessage(a), a.view)
	}
	// The same head is not posted twice.
	a.handleCommand("/review post")
	finishPost(t, a, "y")
	if len(f.comments) != 1 || !strings.Contains(a.codeReviewPostView(), "already posted") {
		t.Fatalf("second post: %d comments\n%s", len(f.comments), a.codeReviewPostView())
	}
}

func TestReviewPostRefusals(t *testing.T) {
	deny := "deny"
	a, _, f := codeReviewApp(t, config.Settings{Review: &config.ReviewSettings{Post: &deny}}, "verdict: comment\nfinding: a.go:2: nit docs: x\n")
	runCodeReview(t, a, "pr 4")
	if strings.Contains(lastSystemMessage(a), "/review post") {
		t.Error("posting offered with review.post=deny")
	}
	a.handleCommand("/review post")
	if a.view != viewChat || !strings.Contains(lastSystemMessage(a), "review.post is deny") {
		t.Fatalf("deny: view %v, %q", a.view, lastSystemMessage(a))
	}
	a, _, f = codeReviewApp(t, config.Settings{}, "verdict: comment\n")
	a.handleCommand("/review post")
	if !strings.Contains(lastSystemMessage(a), "run /review pr first") {
		t.Fatalf("nothing reviewed: %q", lastSystemMessage(a))
	}
	runCodeReview(t, a, "") // a working tree review
	a.handleCommand("/review post")
	if a.view != viewChat || !strings.Contains(lastSystemMessage(a), "only the review of a pull request") {
		t.Fatalf("working tree: view %v, %q", a.view, lastSystemMessage(a))
	}
	// A rule that denies the post wins over the person's yes.
	a, _, f = codeReviewApp(t, config.Settings{Permissions: config.PermissionRules{Deny: []string{"ReviewPost"}}}, "verdict: comment\nfinding: a.go:2: nit docs: x\n")
	runCodeReview(t, a, "pr 4")
	a.handleCommand("/review post")
	finishPost(t, a, "y")
	if len(f.comments) != 0 || !strings.Contains(a.codeReviewPostView(), "denied") {
		t.Fatalf("a deny rule did not hold: %d comments\n%s", len(f.comments), a.codeReviewPostView())
	}
	// An incomplete review is not posted.
	a, _, _ = codeReviewApp(t, config.Settings{}, "no verdict here")
	runCodeReview(t, a, "pr 4")
	a.handleCommand("/review post")
	if a.view != viewChat || !strings.Contains(lastSystemMessage(a), "no readable verdict") {
		t.Fatalf("incomplete: view %v, %q", a.view, lastSystemMessage(a))
	}
}
