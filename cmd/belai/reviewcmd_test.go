package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/forge"
	"github.com/vulnetix/belai/internal/review"
)

func TestParseReviewArgs(t *testing.T) {
	a, _, ok := parseReviewArgs([]string{"-pr", "7", "-json", "-comment", "-rubric", "r.txt", "-force"}, &bytes.Buffer{})
	if !ok || a.pr != "7" || !a.json || !a.comment || a.rubric != "r.txt" || !a.force {
		t.Fatalf("%+v %v", a, ok)
	}
	a, _, ok = parseReviewArgs([]string{"-base", "main", "feature"}, &bytes.Buffer{})
	if !ok || a.base != "main" || a.branch != "feature" {
		t.Fatalf("%+v %v", a, ok)
	}
	var errBuf bytes.Buffer
	if _, code, ok := parseReviewArgs([]string{"one", "two"}, &errBuf); ok || code != 2 || !strings.Contains(errBuf.String(), "usage: belai review") {
		t.Fatalf("two branches: code %d ok %v", code, ok)
	}
	if _, code, ok := parseReviewArgs([]string{"-nope"}, &bytes.Buffer{}); ok || code != 2 {
		t.Fatalf("unknown flag: code %d ok %v", code, ok)
	}
	if _, code, ok := parseReviewArgs([]string{"-h"}, &bytes.Buffer{}); ok || code != 0 {
		t.Fatalf("-h: code %d ok %v", code, ok)
	}
}

func TestReviewTarget(t *testing.T) {
	cases := []struct {
		a    reviewArgs
		want review.Target
		err  bool
	}{
		{reviewArgs{}, review.Target{Kind: review.KindWorkdir}, false},
		{reviewArgs{pr: "7"}, review.Target{Kind: review.KindPR, Ref: "7"}, false},
		{reviewArgs{pr: "current", comment: true}, review.Target{Kind: review.KindPR, Ref: "current"}, false},
		{reviewArgs{base: "main"}, review.Target{Kind: review.KindBranch, Base: "main"}, false},
		{reviewArgs{branch: "f", base: "main"}, review.Target{Kind: review.KindBranch, Ref: "f", Base: "main"}, false},
		{reviewArgs{pr: "7", base: "main"}, review.Target{}, true},
		{reviewArgs{pr: "7", branch: "f"}, review.Target{}, true},
		{reviewArgs{comment: true}, review.Target{}, true},
		{reviewArgs{base: "main", comment: true}, review.Target{}, true},
	}
	for i, c := range cases {
		got, err := reviewTarget(c.a)
		if (err != nil) != c.err || got != c.want {
			t.Errorf("case %d: %+v, %v", i, got, err)
		}
	}
}

type scriptedReviewer struct {
	reply string
	err   error
	calls int
}

func (s *scriptedReviewer) Review(context.Context, string) (string, error) {
	s.calls++
	return s.reply, s.err
}

type commentForge struct {
	forge.Provider
	comments []string
}

func (c *commentForge) CommentPR(_ context.Context, _ string, _ int, body string) error {
	c.comments = append(c.comments, body)
	return nil
}
func (c *commentForge) Kind() string  { return forge.KindGitHub }
func (c *commentForge) Noun() string  { return "PR" }
func (c *commentForge) Label() string { return "fake" }
func (c *commentForge) PRForBranch(context.Context, string, string) (*forge.PR, error) {
	return &forge.PR{Number: 7}, nil
}
func (c *commentForge) DiffPR(context.Context, string, int) (forge.PRDiff, error) {
	return forge.PRDiff{
		Diff:    "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1,2 @@\n package a\n+var x = 1\n",
		HeadSHA: strings.Repeat("ab", 20),
	}, nil
}

func reviewRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	env := append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x.test", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x.test")
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "."}, {"commit", "-q", "-m", "base", "--allow-empty"}} {
		if args[0] == "add" {
			if err := os.WriteFile(dir+"/a.go", []byte("package a\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.WriteFile(dir+"/a.go", []byte("package a\nvar x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func newReviewRun(t *testing.T, rv review.Reviewer, a reviewArgs, target review.Target, s config.Settings) (reviewRun, *commentForge) {
	t.Helper()
	dir := reviewRepo(t)
	f := &commentForge{}
	return reviewRun{
		opts: review.Options{
			Settings:  s,
			Collector: review.Collector{Dir: dir, Provider: func(context.Context) (forge.Provider, error) { return f, nil }},
			Reviewer:  rv, Store: &review.Store{Dir: t.TempDir()},
		},
		target: target, args: a, dir: dir,
		provider: func() (forge.Provider, error) { return f, nil },
	}, f
}

func TestRunReviewExitCodes(t *testing.T) {
	cases := []struct {
		reply string
		want  int
	}{
		{"verdict: approve\n", 0},
		{"verdict: comment\nfinding: a.go:2: nit docs: x\n", 0},
		{"verdict: request_changes\nfinding: a.go:2: major correctness: broken\n", 1},
		{"verdict: request_changes\n", 0}, // no finding it can point to, so only a comment
	}
	for _, c := range cases {
		r, _ := newReviewRun(t, &scriptedReviewer{reply: c.reply}, reviewArgs{}, review.Target{Kind: review.KindWorkdir}, config.Settings{})
		var out, errb bytes.Buffer
		if got := runReview(context.Background(), r, &out, &errb); got != c.want {
			t.Errorf("%q: exit %d, want %d\nout: %s\nerr: %s", c.reply, got, c.want, out.String(), errb.String())
		}
		if !strings.Contains(out.String(), "(advisory)") {
			t.Errorf("no advisory verdict in %q", out.String())
		}
	}
	// An error is 2.
	r, _ := newReviewRun(t, &scriptedReviewer{err: errors.New("provider down")}, reviewArgs{}, review.Target{Kind: review.KindWorkdir}, config.Settings{})
	var errb bytes.Buffer
	if got := runReview(context.Background(), r, &bytes.Buffer{}, &errb); got != 2 || !strings.Contains(errb.String(), "provider down") {
		t.Errorf("error exit %d: %s", got, errb.String())
	}
	off := false
	r, _ = newReviewRun(t, &scriptedReviewer{reply: "verdict: approve"}, reviewArgs{}, review.Target{Kind: review.KindWorkdir}, config.Settings{Review: &config.ReviewSettings{Enabled: &off}})
	if got := runReview(context.Background(), r, &bytes.Buffer{}, &bytes.Buffer{}); got != 2 {
		t.Errorf("disabled review exit %d", got)
	}
}

func TestRunReviewJSON(t *testing.T) {
	r, _ := newReviewRun(t, &scriptedReviewer{reply: "verdict: comment\nfinding: a.go:2: minor docs: x\nfix: y\n"}, reviewArgs{json: true}, review.Target{Kind: review.KindWorkdir}, config.Settings{})
	var out bytes.Buffer
	if got := runReview(context.Background(), r, &out, &bytes.Buffer{}); got != 0 {
		t.Fatalf("exit %d", got)
	}
	var v struct {
		Verdict  string `json:"verdict"`
		Cached   bool   `json:"cached"`
		Findings []struct {
			File, Severity, Rubric, Body, Fix string
			Line                              int
		} `json:"findings"`
	}
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if v.Verdict != "comment" || v.Cached || len(v.Findings) != 1 || v.Findings[0].File != "a.go" || v.Findings[0].Fix != "y" {
		t.Fatalf("%+v", v)
	}
}

func TestRunReviewComment(t *testing.T) {
	allow, deny, ask := "allow", "deny", "ask"
	pr := review.Target{Kind: review.KindPR, Ref: "7"}
	reply := "verdict: comment\nfinding: a.go:2: minor docs: x\n"
	t.Run("allowed", func(t *testing.T) {
		r, f := newReviewRun(t, &scriptedReviewer{reply: reply}, reviewArgs{comment: true}, pr, config.Settings{Review: &config.ReviewSettings{Post: &allow}})
		var errb bytes.Buffer
		if got := runReview(context.Background(), r, &bytes.Buffer{}, &errb); got != 0 || len(f.comments) != 1 {
			t.Fatalf("exit %d, %d comments: %s", got, len(f.comments), errb.String())
		}
		if !strings.HasPrefix(f.comments[0], review.Marker(strings.Repeat("ab", 20))) {
			t.Errorf("comment: %q", f.comments[0])
		}
		// A second run is answered from the store and does not post again.
		var errb2 bytes.Buffer
		if got := runReview(context.Background(), r, &bytes.Buffer{}, &errb2); got != 2 || len(f.comments) != 1 || !strings.Contains(errb2.String(), "already posted") {
			t.Fatalf("repeat: exit %d, %d comments: %s", got, len(f.comments), errb2.String())
		}
	})
	for name, s := range map[string]config.Settings{
		"ask":     {Review: &config.ReviewSettings{Post: &ask}},
		"default": {},
		"deny":    {Review: &config.ReviewSettings{Post: &deny}},
	} {
		t.Run(name+" never posts without a person", func(t *testing.T) {
			r, f := newReviewRun(t, &scriptedReviewer{reply: reply}, reviewArgs{comment: true}, pr, s)
			var errb bytes.Buffer
			if got := runReview(context.Background(), r, &bytes.Buffer{}, &errb); got != 2 || len(f.comments) != 0 || !strings.Contains(errb.String(), "not posted") {
				t.Fatalf("exit %d, %d comments: %s", got, len(f.comments), errb.String())
			}
		})
	}
	t.Run("without -comment nothing is posted", func(t *testing.T) {
		r, f := newReviewRun(t, &scriptedReviewer{reply: reply}, reviewArgs{}, pr, config.Settings{Review: &config.ReviewSettings{Post: &allow}})
		if got := runReview(context.Background(), r, &bytes.Buffer{}, &bytes.Buffer{}); got != 0 || len(f.comments) != 0 {
			t.Fatalf("exit %d, %d comments", got, len(f.comments))
		}
	})
}

func TestReviewIsListedAndDocumentedInHelp(t *testing.T) {
	found := false
	for _, c := range topCommands {
		found = found || c.name == "review"
	}
	if !found {
		t.Fatal("belai review is not in the command list")
	}
	if got, ok := helpTarget([]string{"review"}); !ok || len(got) != 2 || got[1] != "-h" {
		t.Fatalf("helpTarget = %v %v", got, ok)
	}
	for _, flag := range []string{"-pr", "-base", "-json", "-comment", "-rubric"} {
		if !strings.Contains(reviewUsage, flag) {
			t.Errorf("usage lacks %s", flag)
		}
	}
}
