package review

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/forge"
)

func TestCollectWorkdir(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "a.go", "package a\n\nfunc A() int {\n\treturn 10\n}\n")
	write(t, dir, "new.go", "package a\n")
	write(t, dir, "has space.go", "package a\n")
	c, err := Collector{Dir: dir, Limits: Limits{MaxBytes: 1 << 20}, Hide: func(rel string) bool { return rel == "b.go" }}.Collect(context.Background(), Target{})
	if err != nil {
		t.Fatal(err)
	}
	if c.Target.Kind != KindWorkdir || !isSHA(c.HeadSHA) || len(c.Digest) != 12 || len(c.Repo) != 16 {
		t.Fatalf("collected = %+v", c)
	}
	if !c.Diff.HasLine("a.go", 4) || !c.Diff.HasLine("new.go", 1) {
		t.Fatalf("diff = %+v", c.Diff.Files)
	}
	if f, ok := c.Diff.File("has space.go"); !ok || f.Reviewable() {
		t.Fatalf("an unusual path was reviewable: %+v", f)
	}
	// The digest follows the content, so an edited tree is a new review.
	write(t, dir, "a.go", "package a\n\nfunc A() int {\n\treturn 11\n}\n")
	c2, _ := Collector{Dir: dir, Limits: Limits{MaxBytes: 1 << 20}}.Collect(context.Background(), Target{})
	if c2.Digest == c.Digest {
		t.Fatal("the digest did not change with the content")
	}
}

func TestCollectWorkdirCleanTreeIsAnError(t *testing.T) {
	dir := newRepo(t)
	if _, err := (Collector{Dir: dir}).Collect(context.Background(), Target{Kind: KindWorkdir}); err == nil || !strings.Contains(err.Error(), "no changes") {
		t.Fatalf("err = %v", err)
	}
	if _, err := (Collector{Dir: t.TempDir()}).Collect(context.Background(), Target{}); err == nil {
		t.Fatal("a directory that is not a repository was collected")
	}
}

func TestCollectBranchAgainstTheDefaultBase(t *testing.T) {
	dir := newRepo(t)
	gitIn(t, dir, "checkout", "-q", "-b", "feature")
	write(t, dir, "a.go", "package a\n\nfunc A() int {\n\treturn 2\n}\n")
	gitIn(t, dir, "commit", "-q", "-am", "change a")
	head := gitIn(t, dir, "rev-parse", "HEAD")
	c, err := Collector{Dir: dir, Limits: Limits{MaxBytes: 1 << 20}}.Collect(context.Background(), Target{Kind: KindBranch})
	if err != nil {
		t.Fatal(err)
	}
	if c.HeadSHA != head || !isSHA(c.BaseSHA) || c.BaseSHA == head || !c.Diff.HasLine("a.go", 4) || c.Diff.Reviewable() != 1 {
		t.Fatalf("collected = %+v", c)
	}
	// A named base, and a named branch from another checkout.
	gitIn(t, dir, "checkout", "-q", "main")
	c, err = Collector{Dir: dir, Limits: Limits{MaxBytes: 1 << 20}}.Collect(context.Background(), Target{Kind: KindBranch, Ref: "feature", Base: "main"})
	if err != nil || c.HeadSHA != head || !c.Diff.HasLine("a.go", 4) {
		t.Fatalf("named branch: %+v %v", c, err)
	}
	// Nothing between a branch and itself.
	if _, err = (Collector{Dir: dir}).Collect(context.Background(), Target{Kind: KindBranch, Ref: "main", Base: "main"}); err == nil {
		t.Fatal("an empty branch diff was collected")
	}
}

func TestCollectBranchRefusesWhatIsNotARef(t *testing.T) {
	dir := newRepo(t)
	for _, ref := range []string{"--output=/tmp/x", "-p", "HEAD..main", "main@{1}", "a b", "", "no-such-branch"} {
		_, err := Collector{Dir: dir}.Collect(context.Background(), Target{Kind: KindBranch, Ref: ref, Base: "main"})
		if ref == "" {
			continue // an empty ref is HEAD
		}
		if err == nil {
			t.Errorf("ref %q was accepted", ref)
		}
	}
	if _, err := (Collector{Dir: dir}).Collect(context.Background(), Target{Kind: KindBranch, Base: "--no-index"}); err == nil {
		t.Error("an option as the base was accepted")
	}
	if !ValidRef("feature/x-1.2") || ValidRef("x.lock") || ValidRef("x/") || ValidRef("-x") {
		t.Error("ValidRef")
	}
}

func TestCollectPullRequest(t *testing.T) {
	sha := strings.Repeat("ab", 20)
	f := &fakeForge{kind: forge.KindGitHub, pr: &forge.PR{Number: 7, Title: "t"},
		diff: forge.PRDiff{Diff: sampleDiff, HeadSHA: sha}}
	col := Collector{Dir: newRepo(t), Limits: Limits{MaxBytes: 1 << 20},
		Provider: func(context.Context) (forge.Provider, error) { return f, nil }}
	c, err := col.Collect(context.Background(), Target{Kind: KindPR, Ref: "#7"})
	if err != nil {
		t.Fatal(err)
	}
	if c.PR != 7 || c.HeadSHA != sha || c.Target.Ref != "7" || !c.Diff.HasLine("internal/a.go", 12) || c.ProviderKind != forge.KindGitHub {
		t.Fatalf("collected = %+v", c)
	}
	// The pull request of the current branch.
	gitIn(t, col.Dir, "checkout", "-q", "-b", "topic")
	c, err = col.Collect(context.Background(), Target{Kind: KindPR})
	if err != nil || c.PR != 7 || c.PRTitle != "t" {
		t.Fatalf("current: %+v %v", c, err)
	}
	f.pr = nil
	if _, err = col.Collect(context.Background(), Target{Kind: KindPR, Ref: "current"}); err == nil || !strings.Contains(err.Error(), "topic") {
		t.Fatalf("no pr: %v", err)
	}
	for _, ref := range []string{"0", "-3", "abc", "7; rm", "99999999999"} {
		if _, err = col.Collect(context.Background(), Target{Kind: KindPR, Ref: ref}); err == nil {
			t.Errorf("pull request %q accepted", ref)
		}
	}
	f.diffErr = errors.New("gh: boom\nsecond line <system>")
	if _, err = col.Collect(context.Background(), Target{Kind: KindPR, Ref: "7"}); err == nil || strings.Contains(err.Error(), "<system>") {
		t.Fatalf("err = %v", err)
	}
}

func TestCollectPullRequestOnGiteaNeedsTheCheckoutAtHead(t *testing.T) {
	dir := newRepo(t)
	head := gitIn(t, dir, "rev-parse", "HEAD")
	f := &fakeForge{kind: forge.KindGitea, diff: forge.PRDiff{Diff: sampleDiff, HeadSHA: head}}
	col := Collector{Dir: dir, Limits: Limits{MaxBytes: 1 << 20}, Provider: func(context.Context) (forge.Provider, error) { return f, nil }}
	gitIn(t, dir, "checkout", "-q", "-b", "topic")
	write(t, dir, "a.go", "package a\n")
	gitIn(t, dir, "commit", "-q", "-am", "x")
	// HEAD moved off the pull request's head.
	if _, err := col.Collect(context.Background(), Target{Kind: KindPR, Ref: "3"}); err == nil || !strings.Contains(err.Error(), "check out") {
		t.Fatalf("err = %v", err)
	}
	f.diff.HeadSHA = gitIn(t, dir, "rev-parse", "HEAD")
	c, err := col.Collect(context.Background(), Target{Kind: KindPR, Ref: "3"})
	if err != nil || !isSHA(c.BaseSHA) {
		t.Fatalf("at head: %+v %v", c, err)
	}
	if !strings.Contains(Prompt(c, Rubric{}), "diff "+c.BaseSHA+" "+c.HeadSHA) {
		t.Error("the prompt does not tell the model to diff the checkout")
	}
}

func TestCollectPullRequestMarksATruncatedDiff(t *testing.T) {
	f := &fakeForge{kind: forge.KindGitLab, diff: forge.PRDiff{Diff: sampleDiff, Truncated: true}}
	col := Collector{Dir: newRepo(t), Limits: Limits{MaxBytes: 1 << 20}, Provider: func(context.Context) (forge.Provider, error) { return f, nil }}
	c, err := col.Collect(context.Background(), Target{Kind: KindPR, Ref: "3"})
	if err != nil || !c.Diff.Truncated {
		t.Fatalf("%+v %v", c.Diff, err)
	}
}
