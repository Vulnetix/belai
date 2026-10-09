package fleet

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/scanartifacts"
)

func roundsPatcher(rounds int) agentprofile.AgentProfile {
	p := patcherProfile()
	p.Workspace = &agentprofile.WorkspaceSpec{Isolation: agentprofile.IsolationWorktree}
	p.Kanban.Security.Rounds = rounds
	return p
}

// scanScript answers each feedback scan from a list: true means the scanner
// still reports the finding.
func scanScript(present ...bool) (func(context.Context, string, string, int, int) (scanFeedback, bool), *int) {
	calls := 0
	return func(_ context.Context, dir, finding string, round, max int) (scanFeedback, bool) {
		i := calls
		calls++
		if i >= len(present) {
			return scanFeedback{}, false
		}
		if !present[i] {
			return scanFeedback{}, true
		}
		return composeFeedback(scanartifacts.ReviewResult{Findings: []scanartifacts.ReviewFinding{
			{ID: finding, Kind: scanartifacts.KindSCA, Package: "lodash", Version: "4.17.0", File: "package.json", Severity: "high"},
		}}, finding, round, max), true
	}, &calls
}

func roundsWorker(t *testing.T, rounds int, turns *[]Turn) (*kanban.Store, *Worker, kanban.Item) {
	t.Helper()
	store, reg := testEnv(t)
	w := newWorker(t, store, reg, roundsPatcher(rounds), func(ctx context.Context, tt Turn) (run.Result, error) {
		*turns = append(*turns, tt)
		return run.Result{StopReason: run.StopComplete, GoalSentinel: "GOAL_COMPLETE", Passes: 2}, nil
	})
	w.Repo = gitRepo(t)
	it := findingCardOn(t, store, w, kanban.Backlog, "vuln")
	return store, w, it
}

func TestPatcherGetsAFeedbackRoundWhileTheScannerStillReportsTheFinding(t *testing.T) {
	var turns []Turn
	store, w, it := roundsWorker(t, 3, &turns)
	w.Scan, _ = scanScript(true, false) // still there after round 1, gone after round 2
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 {
		t.Fatalf("%d turns, want 2", len(turns))
	}
	if turns[0].Feedback != "" || turns[0].Round != 0 {
		t.Fatalf("the first turn carries feedback: %+v", turns[0])
	}
	fb := turns[1].Feedback
	for _, want := range []string{"Round 1 of 3", "GHSA-aaaa-bbbb-cccc", "package: lodash", "version: 4.17.0", "Change your approach"} {
		if !strings.Contains(fb, want) {
			t.Errorf("feedback lacks %q:\n%s", want, fb)
		}
	}
	if turns[1].Round != 2 || turns[1].SessionID == turns[0].SessionID || turns[1].Claim != turns[0].Claim {
		t.Fatalf("round two: %+v", turns[1])
	}
	got, _ := store.Get(it.ID)
	if got.List != kanban.Review || got.Verdict != kanban.VerdictFixed || !strings.Contains(got.LastNote(), "confirmed the fix after 2 rounds") {
		t.Fatalf("card %+v (%q)", got, got.LastNote())
	}
}

func TestPatcherFailsTheAttemptWhenTheScannerStillReportsItAfterTheLastRound(t *testing.T) {
	var turns []Turn
	store, w, it := roundsWorker(t, 3, &turns)
	scan, calls := scanScript(true, true, true)
	w.Scan = scan
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(turns) != 3 || *calls != 3 {
		t.Fatalf("%d turns and %d scans, want 3 and 3", len(turns), *calls)
	}
	got, _ := store.Get(it.ID)
	if got.List != kanban.Backlog || got.Attempts != 1 || got.Verdict != "" || !strings.Contains(got.LastNote(), "still reports GHSA-aaaa-bbbb-cccc") {
		t.Fatalf("a model's word beat the scanner: %+v (%q)", got, got.LastNote())
	}
}

func TestPatcherWithoutAScanAnswerRunsOneTurnAsBefore(t *testing.T) {
	var turns []Turn
	store, w, it := roundsWorker(t, 3, &turns)
	w.Scan, _ = scanScript() // no answer: no CLI, or the scan failed
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(it.ID)
	if len(turns) != 1 || got.List != kanban.Review {
		t.Fatalf("%d turns, card %+v", len(turns), got)
	}
}

func TestOneRoundNeverScans(t *testing.T) {
	var turns []Turn
	_, w, _ := roundsWorker(t, 1, &turns)
	scan, calls := scanScript(true)
	w.Scan = scan
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if *calls != 0 || len(turns) != 1 {
		t.Fatalf("%d scans, %d turns", *calls, len(turns))
	}
}

func TestPatcherThatRecordsANonFixVerdictIsNotScanned(t *testing.T) {
	store, reg := testEnv(t)
	w := newWorker(t, store, reg, roundsPatcher(3), nil)
	w.Repo = gitRepo(t)
	w.Runner = verdictRunner(store, map[string]any{"verdict": "needs_human", "justification": "licence question"}, true)
	scan, calls := scanScript(true)
	w.Scan = scan
	it := findingCardOn(t, store, w, kanban.Backlog, "vuln")
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(it.ID)
	if *calls != 0 || got.List != kanban.Review || got.Verdict != kanban.VerdictNeedsHuman {
		t.Fatalf("%d scans, card %+v", *calls, got)
	}
}

func TestOnlyAnSCAFindingInAWorktreeGetsRounds(t *testing.T) {
	w := &Worker{Profile: roundsPatcher(3)}
	ws := &Workspace{Worktree: true}
	if w.maxRounds(kanban.Item{Finding: "CVE-1"}, ws) != 3 {
		t.Fatal("an SCA finding did not get its rounds")
	}
	for name, c := range map[string]struct {
		it kanban.Item
		ws *Workspace
	}{
		"hand-filed card":  {kanban.Item{}, ws},
		"sast finding":     {kanban.Item{Finding: "sast:rule:abcd1234"}, ws},
		"enrichment item":  {kanban.Item{Finding: "sweep:abcdef123456"}, ws},
		"shared workspace": {kanban.Item{Finding: "CVE-1"}, &Workspace{}},
		"no workspace":     {kanban.Item{Finding: "CVE-1"}, nil},
	} {
		if n := w.maxRounds(c.it, c.ws); n != 1 {
			t.Errorf("%s: %d rounds, want 1", name, n)
		}
	}
}

func TestComposeFeedbackIsIdentifiersAndCounts(t *testing.T) {
	res := scanartifacts.ReviewResult{Findings: []scanartifacts.ReviewFinding{
		{ID: "CVE-1", Kind: scanartifacts.KindSCA, Package: "a", Version: "1.0.0", Severity: "high"},
		{ID: "CVE-2", Kind: scanartifacts.KindSCA, Package: "b"},
		{ID: "sast:r:1", Kind: scanartifacts.KindSAST},
	}}
	fb := composeFeedback(res, "CVE-1", 1, 3)
	if !fb.present || !strings.Contains(fb.text, "findings still open in this worktree: 2") {
		t.Fatalf("%+v", fb)
	}
	if got := composeFeedback(res, "CVE-9", 1, 3); got.present || got.text != "" {
		t.Fatalf("an absent finding produced feedback: %+v", got)
	}
}

func TestRemoveScanArtefactsNeverFollowsALink(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "keep"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, ".vulnetix")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip("no symlinks here")
	}
	removeScanArtefacts(link)
	if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Fatal("the link's target was deleted")
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatal("the link was not removed")
	}
	real := filepath.Join(dir, "real", ".vulnetix")
	_ = os.MkdirAll(real, 0o700)
	_ = os.WriteFile(filepath.Join(real, "memory.yaml"), nil, 0o600)
	removeScanArtefacts(real)
	if _, err := os.Lstat(real); !os.IsNotExist(err) {
		t.Fatal("the scan directory was not removed")
	}
}

// A provider that refuses the very first call (rate limit, quota, outage) says
// nothing about the card, so the attempt is not counted and the card goes back.
func TestAProviderOutageBeforeAnyWorkIsNotAnAttempt(t *testing.T) {
	store, reg := testEnv(t)
	w := newWorker(t, store, reg, roundsPatcher(1), func(_ context.Context, _ Turn) (run.Result, error) {
		return run.Result{StopReason: run.StopError, Passes: 0}, errors.New(`provider returned 503: {"errors":[{"code":503,"message":"unavailable"}]}`)
	})
	w.Repo = gitRepo(t)
	it := findingCardOn(t, store, w, kanban.Backlog, "vuln")
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(it.ID)
	if got.List != kanban.Backlog || got.Attempts != 0 || !strings.Contains(got.LastNote(), "not counted as an attempt") {
		t.Fatalf("an outage cost the card an attempt: %+v (%q)", got, got.LastNote())
	}
}

func TestAnErrorAfterWorkStillCountsAsAnAttempt(t *testing.T) {
	store, reg := testEnv(t)
	w := newWorker(t, store, reg, roundsPatcher(1), func(_ context.Context, _ Turn) (run.Result, error) {
		return run.Result{StopReason: run.StopError, Passes: 3}, errors.New(`provider returned 503: unavailable`)
	})
	w.Repo = gitRepo(t)
	it := findingCardOn(t, store, w, kanban.Backlog, "vuln")
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(it.ID)
	if got.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (%q)", got.Attempts, got.LastNote())
	}
}

// An outage before any work leaves a verifier's card where it was claimed from
// with its labels as they were: the failure route would hand it back to a patcher.
func TestAProviderOutageLeavesAVerifierCardInReview(t *testing.T) {
	store, reg := testEnv(t)
	w := newWorker(t, store, reg, verifierProfile(), func(_ context.Context, _ Turn) (run.Result, error) {
		return run.Result{StopReason: run.StopError, Passes: 0}, errors.New(`provider returned 503: {"errors":[{"code":503,"message":"unavailable"}]}`)
	})
	it := findingCardOn(t, store, w, kanban.Review, "needs-verify")
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(it.ID)
	if got.List != kanban.Review || got.Attempts != 0 || !slices.Contains(got.Labels, kanban.LabelNeedsVerify) || slices.Contains(got.Labels, kanban.LabelVuln) {
		t.Fatalf("an outage moved the verifier's card: %+v (%q)", got, got.LastNote())
	}
}
