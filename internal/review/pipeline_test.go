package review

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/forge"
)

// collected runs Collect against a fake forge, the package's other way to
// gather a change. It covers the whole Collect pipeline (workdir, branch, PR)
// without a git subprocess, which cannot run under the sandbox's read-only
// .belai-git remap; Collector.Git stubs the calls Collect makes.
type stubGit struct {
	out  map[string]string
	errs map[string]error
}

func (s stubGit) git(_ context.Context, _ string, args ...string) ([]byte, error) {
	key := strings.Join(args, " ")
	if s.errs != nil {
		if err, ok := s.errs[key]; ok {
			return nil, err
		}
	}
	if s.out != nil {
		if out, ok := s.out[key]; ok {
			return []byte(out), nil
		}
	}
	return nil, errors.New("stub git: unexpected " + key)
}

// The exact arguments Collect passes to git, keyed to stub answers.
const (
	revParseHEAD = "rev-parse --verify --quiet HEAD^{commit}"
	revParseRef  = "rev-parse --verify --quiet ref^{commit}"
	revParseBase = "rev-parse --verify --quiet base^{commit}"
	// mergeBase and branchDiff are prefixes; the resolved commit ids follow.
	mergeBase     = "merge-base"
	branchDiff    = "diff --no-color --no-ext-diff --no-textconv --no-renames -U3"
	symbolicShort = "symbolic-ref --quiet --short HEAD"
)

func TestCollectBranchThroughStubGit(t *testing.T) {
	sha := strings.Repeat("ef", 20)
	// The merge-base garbage case.
	g := stubGit{out: map[string]string{
		revParseRef:  sha,
		revParseBase: sha,
		mergeBase:    "mb-not-a-sha", // no shared history
	}}
	c := Collector{Dir: t.TempDir(), Git: g.git}
	if _, err := c.Collect(context.Background(), Target{Kind: KindBranch, Ref: "ref", Base: "base"}); err == nil ||
		!strings.Contains(err.Error(), "share no history") {
		t.Fatalf("merge-base garbage: %v", err)
	}
	// A merge base that is a commit id, and a diff with one file. The stub keys
	// carry the resolved commit ids Collect passes to git.
	mb := strings.Repeat("ab", 20)
	g.out[mergeBase+" "+sha+" "+sha] = mb
	g.out[branchDiff+" "+mb+" "+sha] = sampleDiff
	col, err := c.Collect(context.Background(), Target{Kind: KindBranch, Ref: "ref", Base: "base"})
	if err != nil {
		t.Fatal(err)
	}
	if col.HeadSHA != sha || col.BaseSHA != strings.Repeat("ab", 20) ||
		col.Diff.Reviewable() == 0 || col.Target.Kind != KindBranch {
		t.Fatalf("collected = %+v", col)
	}
}

func TestCollectPullRequestThroughStubGit(t *testing.T) {
	sha := strings.Repeat("cd", 20)
	f := &fakeForge{kind: forge.KindGitHub, pr: &forge.PR{Number: 7, Title: "t"},
		diff: forge.PRDiff{Diff: sampleDiff, HeadSHA: sha}}
	g := stubGit{out: map[string]string{symbolicShort: "topic"}}
	c := Collector{Dir: t.TempDir(), Git: g.git, Limits: Limits{MaxBytes: 1 << 20},
		Provider: func(context.Context) (forge.Provider, error) { return f, nil }}
	// "current" resolves the pull request of the current branch.
	col, err := c.Collect(context.Background(), Target{Kind: KindPR})
	if err != nil || col.PR != 7 || col.PRTitle != "t" || col.ProviderKind != forge.KindGitHub {
		t.Fatalf("current: %+v %v", col, err)
	}
	// A pull request whose diff is empty is refused.
	f.diff = forge.PRDiff{HeadSHA: sha}
	if _, err = c.Collect(context.Background(), Target{Kind: KindPR, Ref: "7"}); err == nil ||
		!strings.Contains(err.Error(), "no changes") {
		t.Fatalf("empty diff: %v", err)
	}
}

func TestCollectWorkdirThroughStubGit(t *testing.T) {
	// workdiff.Collect does its own git, so a clean non-repository is refused
	// before the stub is reached.
	g := stubGit{out: map[string]string{revParseHEAD: strings.Repeat("ab", 20)}}
	c := Collector{Dir: t.TempDir(), Git: g.git}
	if _, err := c.Collect(context.Background(), Target{Kind: KindWorkdir}); err == nil {
		t.Fatal("a non-repository working tree was collected")
	}
}

func TestProviderForReportsNoRemote(t *testing.T) {
	// A directory with no origin remote and no forge CLI on PATH.
	dir := filepath.Join(t.TempDir(), "not-a-repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if p, err := ProviderFor(dir); p != nil || err == nil ||
		!strings.Contains(err.Error(), "no origin remote") {
		t.Fatalf("ProviderFor = %v, %v", p, err)
	}
}

func TestOpenStoreReturnsTheStateStore(t *testing.T) {
	t.Setenv("BELAI_HOME", filepath.Join(t.TempDir(), "state"))
	s, err := OpenStore()
	if err != nil || s == nil || !strings.HasSuffix(s.Dir, "reviews") {
		t.Fatalf("OpenStore = %+v %v", s, err)
	}
}

func TestRunErrorsBeforeTheReviewerIsCalled(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		off := false
		o := Options{Settings: config.Settings{Review: &config.ReviewSettings{Enabled: &off}}}
		rv := &fakeReviewer{}
		o.Reviewer = rv
		if _, _, err := Run(context.Background(), o, Target{}); !errors.Is(err, ErrDisabled) || rv.calls != 0 {
			t.Fatalf("err = %v calls %d", err, rv.calls)
		}
	})
	t.Run("no reviewer", func(t *testing.T) {
		o := Options{Settings: config.Settings{}}
		if _, _, err := Run(context.Background(), o, Target{}); err == nil ||
			!strings.Contains(err.Error(), "no reviewer") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("collect fails", func(t *testing.T) {
		o := Options{Settings: config.Settings{}, Reviewer: &fakeReviewer{},
			Collector: Collector{Dir: t.TempDir(), Git: stubGit{out: map[string]string{}}.git}}
		if _, _, err := Run(context.Background(), o, Target{Kind: KindBranch, Ref: "x", Base: "y"}); err == nil {
			t.Fatal("a failed collection passed")
		}
	})
}

func TestRubricIDsListsEveryCriterion(t *testing.T) {
	ids := RubricIDs()
	if len(ids) != len(Criteria) {
		t.Fatalf("RubricIDs = %d ids for %d criteria", len(ids), len(Criteria))
	}
	for i, c := range Criteria {
		if ids[i] != c.ID {
			t.Fatalf("ids[%d] = %q, criterion %q", i, ids[i], c.ID)
		}
	}
}

func TestFormatRendersEachShape(t *testing.T) {
	sha := strings.Repeat("ab", 20)
	pr := Result{Target: Target{Kind: KindPR}, PR: 9, Repo: strings.Repeat("0", 16),
		HeadSHA: sha, Files: 2, Verdict: VerdictApprove, Findings: []Finding{
			{File: "a.go", Line: 4, Severity: SeverityMajor, Rubric: "correctness", Body: "b", Fix: "f"},
		}}
	s := Format(pr)
	for _, want := range []string{"review of pull request #9: 2 file(s)", "verdict: approve (advisory)",
		"1. a.go:4 major/correctness: b", "   fix: f"} {
		if !strings.Contains(s, want) {
			t.Errorf("format lacks %q:\n%s", want, s)
		}
	}
	// A branch result with a stored verdict and a dropped line.
	br := Result{Target: Target{Kind: KindBranch}, HeadSHA: sha, Repo: strings.Repeat("1", 16),
		Files: 1, Verdict: VerdictRequestChanges, Cached: true, Dropped: 3, Skipped: 1,
		Findings: []Finding{{File: "b.go", Line: 1, Severity: SeverityNit, Rubric: "docs", Body: "n"}}}
	s = Format(br)
	for _, want := range []string{"review of branch " + sha[:12], "verdict: request_changes (advisory)",
		"1. b.go:1 nit/docs: n", "(stored review of this head; run it again to replace it)",
		"3 finding line(s) did not match", "1 changed file(s) were not reviewed"} {
		if !strings.Contains(s, want) {
			t.Errorf("format lacks %q:\n%s", want, s)
		}
	}
	// An empty workdir result with no findings and no verdict.
	s = Format(Result{})
	for _, want := range []string{"review of working tree: 0 file(s)", "verdict:  (advisory)", "no findings"} {
		if !strings.Contains(s, want) {
			t.Errorf("format lacks %q:\n%s", want, s)
		}
	}
}
