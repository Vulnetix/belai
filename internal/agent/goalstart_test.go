package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
)

// TestJoinGoalDraftNamesTheFailure checks that a failed draft says why it
// failed — a bare "drafting failed" left a slow model's timeout undiagnosable —
// and that a provider failure carries its status but never its body.
func TestJoinGoalDraftNamesTheFailure(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{fmt.Errorf("goal contract draft: %w", context.DeadlineExceeded), "timed out after 3s"},
		{rolemanager.ErrGoalDraftUnusable, "draft was unusable"},
		{fmt.Errorf("goal contract draft: %w", &run.ProviderError{Status: 503, Body: "secret body"}), "provider returned 503"},
		{fmt.Errorf("goal contract draft: %w", &run.ProviderError{Err: errors.New("dial tcp")}), "provider unreachable"},
		{errors.New("other"), "goal contract drafting failed; carrying the raw prompt"},
	}
	s := &Session{}
	for _, c := range cases {
		p := &pendingDraft{ch: make(chan goalDraft, 1), cancel: func(error) {}, start: time.Now().Add(-3 * time.Second)}
		p.ch <- goalDraft{err: c.err}
		var warnings []string
		got, pending := s.joinGoalDraft(p, "ship it", func(e Event) { warnings = append(warnings, e.Warning) })
		if got != "ship it" || pending {
			t.Fatalf("%v: goal text = %q, want the raw prompt", c.err, got)
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], c.want) {
			t.Fatalf("%v: warnings = %q, want one containing %q", c.err, warnings, c.want)
		}
		if strings.Contains(warnings[0], "secret body") {
			t.Fatalf("warning leaked the provider body: %q", warnings[0])
		}
	}
}

// draftClassifier answers a goal-contract draft after delay, or fails when
// ctx ends first. It records the cause the draft was cancelled with.
type draftClassifier struct {
	delay time.Duration
	cause chan error
}

func (d draftClassifier) Classify(ctx context.Context, p rolemanager.ClassifierPayload) (string, error) {
	select {
	case <-time.After(d.delay):
		return "## Verification surface\n- run the tests", nil
	case <-ctx.Done():
		if d.cause != nil {
			d.cause <- context.Cause(ctx)
		}
		return "", ctx.Err()
	}
}

// TestGoalDraftDoneBeforeTheLoopIsCarried pins the fast path: a draft that
// finished while exploration ran is taken as the loop starts.
func TestGoalDraftDoneBeforeTheLoopIsCarried(t *testing.T) {
	s := &Session{}
	p := s.startGoalDraft(context.Background(), rolemanager.NewPipeline(draftClassifier{delay: 10 * time.Millisecond}), "ship it")
	time.Sleep(150 * time.Millisecond) // exploration
	var warnings []string
	got, pending := s.joinGoalDraft(p, "ship it", func(e Event) { warnings = append(warnings, e.Warning) })
	if pending || len(warnings) != 0 || !strings.Contains(got, "Objective:\nship it") || !strings.Contains(got, "Verification surface") {
		t.Fatalf("goal = %q pending = %v warnings = %q, want the drafted contract", got, pending, warnings)
	}
}

// TestGoalDraftNeverHoldsTheLoop pins the regression: a draft still running
// when the loop starts costs no wait at all. The loop starts on the raw
// prompt and the draft keeps running rather than being cancelled.
func TestGoalDraftNeverHoldsTheLoop(t *testing.T) {
	cause := make(chan error, 1)
	s := &Session{}
	p := s.startGoalDraft(context.Background(), rolemanager.NewPipeline(draftClassifier{delay: time.Minute, cause: cause}), "ship it")
	defer p.cancel(nil)
	start := time.Now()
	var warnings []string
	got, pending := s.joinGoalDraft(p, "ship it", func(e Event) { warnings = append(warnings, e.Warning) })
	if waited := time.Since(start); waited > 50*time.Millisecond {
		t.Fatalf("join waited %s, want no wait", waited)
	}
	if got != "ship it" || !pending || len(warnings) != 0 {
		t.Fatalf("goal = %q pending = %v warnings = %q, want the raw prompt, pending, no warning", got, pending, warnings)
	}
	select {
	case c := <-cause:
		t.Fatalf("the draft was cancelled (%v) when the loop started", c)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestLateGoalDraftIsAdoptedAsADirective checks a draft that lands after the
// loop started: it becomes the evaluator's goal text and one sealed directive,
// once, and the pending draft is cleared.
func TestLateGoalDraftIsAdoptedAsADirective(t *testing.T) {
	s := &Session{}
	p := s.startGoalDraft(context.Background(), rolemanager.NewPipeline(draftClassifier{delay: 30 * time.Millisecond}), "ship it")
	if _, pending := s.joinGoalDraft(p, "ship it", func(Event) {}); !pending {
		t.Fatal("draft finished instantly; the test needs it still running")
	}
	s.turnDraft = p
	l := passLedger{goalText: "ship it"}
	if turns := s.adoptLateGoalDraft(&l, "ship it", func(Event) {}); turns != nil {
		t.Fatalf("adopted %d turns before the draft landed", len(turns))
	}
	time.Sleep(150 * time.Millisecond)
	turns := s.adoptLateGoalDraft(&l, "ship it", func(Event) {})
	if len(turns) != 2 || !strings.Contains(turns[0].Directive, "Verification surface") || !strings.Contains(turns[0].Directive, goalContractNote) {
		t.Fatalf("turns = %+v, want one sealed contract directive", turns)
	}
	if !strings.Contains(l.goalText, "Verification surface") {
		t.Fatalf("evaluator goal text = %q, want the contract", l.goalText)
	}
	if s.turnDraft != nil || s.adoptLateGoalDraft(&l, "ship it", func(Event) {}) != nil {
		t.Fatal("the draft was adopted more than once")
	}
}

// TestLateGoalDraftFailureIsReportedAndDropped checks a draft that fails after
// the loop started: it is named in a warning and adds no directive.
func TestLateGoalDraftFailureIsReportedAndDropped(t *testing.T) {
	s := &Session{}
	p := &pendingDraft{ch: make(chan goalDraft, 1), cancel: func(error) {}, start: time.Now()}
	p.ch <- goalDraft{err: rolemanager.ErrGoalDraftUnusable}
	s.turnDraft = p
	l := passLedger{goalText: "ship it"}
	var warnings []string
	if turns := s.adoptLateGoalDraft(&l, "ship it", func(e Event) { warnings = append(warnings, e.Warning) }); turns != nil {
		t.Fatalf("a failed draft added %d turns", len(turns))
	}
	if len(warnings) != 1 || l.goalText != "ship it" || s.turnDraft != nil {
		t.Fatalf("warnings = %q goal = %q, want one warning and the raw goal", warnings, l.goalText)
	}
}
