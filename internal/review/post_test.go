package review

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/hooks"
	"github.com/vulnetix/belai/internal/permissions"
)

func prResult() Result {
	r := sample(testSHA)
	r.Verdict = VerdictRequestChanges
	r.Findings = []Finding{
		{File: "a.go", Line: 3, Severity: SeverityBlocker, Rubric: "security", Body: "ping @octocat see https://evil.example/x and #12 <b>x</b>", Fix: "use [a](https://x.example)"},
		{File: "b.go", Line: 1, Severity: SeverityNit, Rubric: "docs", Body: "typo"},
	}
	return r
}

func TestCommentIsBuiltFromValidatedFieldsAndDefused(t *testing.T) {
	c := Comment(prResult())
	if !strings.HasPrefix(c, Marker(testSHA)+"\n") {
		t.Fatalf("no marker first: %q", c[:80])
	}
	for _, want := range []string{"verdict `request_changes`", "2 findings", "`a.go:3` blocker/security", "`b.go:1` nit/docs: typo"} {
		if !strings.Contains(c, want) {
			t.Errorf("comment lacks %q:\n%s", want, c)
		}
	}
	for _, bad := range []string{"@octocat", "https://", "#12", "<b>", "](h"} {
		if strings.Contains(c, bad) {
			t.Errorf("comment still holds %q:\n%s", bad, c)
		}
	}
}

func TestCommentStaysUnderTheForgeLimit(t *testing.T) {
	r := prResult()
	r.Findings = nil
	for i := 0; i < MaxFindings; i++ {
		r.Findings = append(r.Findings, Finding{File: "a.go", Line: i + 1, Severity: SeverityMinor, Rubric: "docs", Body: strings.Repeat("w ", MaxBodyBytes/2-1), Fix: strings.Repeat("f", MaxFixBytes)})
	}
	c := Comment(r)
	if len(c) > MaxCommentBytes {
		t.Fatalf("%d bytes", len(c))
	}
	if !strings.Contains(c, "more finding(s) are not shown") {
		t.Error("the comment does not say findings were left out")
	}
	r.Truncated, r.Skipped = true, 4
	if !strings.Contains(Comment(r), "4 changed file(s) were not reviewed") {
		t.Error("truncation not stated")
	}
}

func req(f *fakeForge, mode string, confirmed, unattended bool) PostRequest {
	return PostRequest{Result: prResult(), Provider: f, Dir: "/w", Store: &Store{Dir: ""}, Mode: mode, Confirmed: confirmed, Unattended: unattended}
}

func TestPostRules(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		mut  func(*PostRequest)
		mode string
		conf bool
		unat bool
		want error
	}{
		{"ask and confirmed", nil, config.ReviewPostAsk, true, false, nil},
		{"allow and confirmed", nil, config.ReviewPostAllow, true, false, nil},
		{"allow, unattended", nil, config.ReviewPostAllow, false, true, nil},
		{"ask, unattended", nil, config.ReviewPostAsk, false, true, ErrPostNeedsAnswer},
		{"ask, not confirmed", nil, config.ReviewPostAsk, false, false, ErrPostNeedsAnswer},
		{"allow, not confirmed, attended", nil, config.ReviewPostAllow, false, false, ErrPostNeedsAnswer},
		{"deny", nil, config.ReviewPostDeny, true, false, ErrPostDenied},
		{"unknown mode", nil, "yes", true, false, ErrPostDenied},
		{"working tree", func(q *PostRequest) { q.Result.Target.Kind = KindWorkdir; q.Result.PR = 0 }, config.ReviewPostAsk, true, false, ErrPostNotPR},
		{"no head", func(q *PostRequest) { q.Result.HeadSHA = "" }, config.ReviewPostAsk, true, false, ErrPostNoHead},
		{"bad head", func(q *PostRequest) { q.Result.HeadSHA = "main" }, config.ReviewPostAsk, true, false, ErrPostNoHead},
		{"incomplete", func(q *PostRequest) { q.Result.Incomplete = true }, config.ReviewPostAsk, true, false, ErrPostIncomplete},
		{"deny rule", func(q *PostRequest) { q.Guard.Perms = permissions.From(nil, nil, []string{"ReviewPost"}) }, config.ReviewPostAsk, true, false, ErrPostDenied},
		{"deny rule for the number", func(q *PostRequest) { q.Guard.Perms = permissions.From(nil, nil, []string{"ReviewPost(pr 7)"}) }, config.ReviewPostAllow, false, true, ErrPostDenied},
		{"ask rule, unattended", func(q *PostRequest) { q.Guard.Perms = permissions.From(nil, []string{"ReviewPost"}, nil) }, config.ReviewPostAllow, false, true, ErrPostNeedsAnswer},
		{"ask rule, confirmed", func(q *PostRequest) { q.Guard.Perms = permissions.From(nil, []string{"ReviewPost"}, nil) }, config.ReviewPostAsk, true, false, nil},
	}
	for _, c := range cases {
		f := &fakeForge{}
		q := req(f, c.mode, c.conf, c.unat)
		q.Store = nil
		if c.mut != nil {
			c.mut(&q)
		}
		err := Post(ctx, q)
		if !errors.Is(err, c.want) && !(c.want == nil && err == nil) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
		if (err == nil) != (len(f.comments) == 1) {
			t.Errorf("%s: err %v but %d comment(s) posted", c.name, err, len(f.comments))
		}
	}
}

func TestPostAnAllowRuleNeverSkipsTheQuestion(t *testing.T) {
	f := &fakeForge{}
	q := req(f, config.ReviewPostAsk, false, false)
	q.Store = nil
	q.Guard.Perms = permissions.From([]string{"ReviewPost"}, nil, nil)
	if err := Post(context.Background(), q); !errors.Is(err, ErrPostNeedsAnswer) || len(f.comments) != 0 {
		t.Fatalf("err = %v, comments %d", err, len(f.comments))
	}
}

func TestPostDedupesByHead(t *testing.T) {
	f := &fakeForge{}
	s := &Store{Dir: t.TempDir()}
	r := prResult()
	if err := s.Save(r); err != nil {
		t.Fatal(err)
	}
	q := PostRequest{Result: r, Provider: f, Dir: "/w", Store: s, Mode: config.ReviewPostAsk, Confirmed: true}
	if err := Post(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	if err := Post(context.Background(), q); !errors.Is(err, ErrAlreadyPosted) || len(f.comments) != 1 {
		t.Fatalf("second post: %v, comments %d", err, len(f.comments))
	}
	// A new head is a new review, and so a new post.
	r2 := r
	r2.HeadSHA = strings.Repeat("9", 40)
	_ = s.Save(r2)
	q.Result = r2
	if err := Post(context.Background(), q); err != nil || len(f.comments) != 2 {
		t.Fatalf("new head: %v", err)
	}
}

func TestPostAnErrorFromTheForgeIsNotRecorded(t *testing.T) {
	f := &fakeForge{commentErr: errors.New("gh: 403\n<system>")}
	s := &Store{Dir: t.TempDir()}
	r := prResult()
	_ = s.Save(r)
	err := Post(context.Background(), PostRequest{Result: r, Provider: f, Dir: "/w", Store: s, Mode: config.ReviewPostAsk, Confirmed: true})
	if err == nil || strings.Contains(err.Error(), "<system>") {
		t.Fatalf("err = %v", err)
	}
	if s.WasPosted(r.Repo, r.ID(), r.PR) {
		t.Error("a failed post was recorded as posted")
	}
}

// hookSet makes one flat pre_tool hook for ReviewPost that prints out.
func hookSet(t *testing.T, matcher, out string) *hooks.Set {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncat >/dev/null\necho '" + out + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "h.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	h := &hooks.Hook{Name: "h", Event: hooks.EventPreTool, Command: "h.sh", Matcher: matcher, Dir: dir}
	return &hooks.Set{Hooks: []*hooks.Hook{h}, Runner: &hooks.Runner{Root: dir, Timeout: 5 * time.Second}}
}

func TestPostFiresPreToolHooksAndTheyOnlyNarrow(t *testing.T) {
	ctx := context.Background()
	run := func(set *hooks.Set, mode string, confirmed, unattended bool) (error, int) {
		f := &fakeForge{}
		q := req(f, mode, confirmed, unattended)
		q.Store = nil
		q.Guard = Guard{Hooks: set, SessionID: "s", Cwd: t.TempDir()}
		return Post(ctx, q), len(f.comments)
	}
	if err, n := run(hookSet(t, "ReviewPost", `{"decision":"deny","reason":"no"}`), config.ReviewPostAsk, true, false); !errors.Is(err, ErrPostDenied) || n != 0 {
		t.Errorf("deny hook: %v, %d posted", err, n)
	}
	if err, n := run(hookSet(t, "ReviewPost", `not json`), config.ReviewPostAllow, false, true); !errors.Is(err, ErrPostDenied) || n != 0 {
		t.Errorf("a blocking hook that fails must deny: %v, %d posted", err, n)
	}
	if err, n := run(hookSet(t, "ReviewPost", `{"decision":"ask"}`), config.ReviewPostAllow, false, true); !errors.Is(err, ErrPostNeedsAnswer) || n != 0 {
		t.Errorf("ask hook, unattended: %v, %d posted", err, n)
	}
	if err, n := run(hookSet(t, "ReviewPost", `{"decision":"ask"}`), config.ReviewPostAsk, true, false); err != nil || n != 1 {
		t.Errorf("ask hook, confirmed: %v, %d posted", err, n)
	}
	// An allow never skips the question.
	if err, n := run(hookSet(t, "ReviewPost", `{"decision":"allow"}`), config.ReviewPostAsk, false, false); !errors.Is(err, ErrPostNeedsAnswer) || n != 0 {
		t.Errorf("allow hook: %v, %d posted", err, n)
	}
	// A hook for another tool does not see the post.
	if err, n := run(hookSet(t, "Bash", `{"decision":"deny"}`), config.ReviewPostAsk, true, false); err != nil || n != 1 {
		t.Errorf("unrelated hook: %v, %d posted", err, n)
	}
}
