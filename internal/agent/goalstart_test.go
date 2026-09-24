package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
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
		got := s.joinGoalDraft(p, "ship it", func(e Event) { warnings = append(warnings, e.Warning) })
		if got != "ship it" {
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

// TestGoalDraftTimeBeforeTheJoinIsFree pins the regression: the draft runs
// alongside exploration, so a draft slower than the grace still lands when the
// loop needs it after it has finished. A single start-anchored deadline used
// to kill a ~30s reasoning-model draft at 20s mid-exploration.
func TestGoalDraftTimeBeforeTheJoinIsFree(t *testing.T) {
	defer func(g time.Duration) { goalDraftGrace = g }(goalDraftGrace)
	goalDraftGrace = 20 * time.Millisecond

	s := &Session{}
	p := s.startGoalDraft(context.Background(), rolemanager.NewPipeline(draftClassifier{delay: 80 * time.Millisecond}), "ship it")
	time.Sleep(150 * time.Millisecond) // exploration, longer than the grace
	var warnings []string
	got := s.joinGoalDraft(p, "ship it", func(e Event) { warnings = append(warnings, e.Warning) })
	if len(warnings) != 0 || !strings.Contains(got, "Objective:\nship it") || !strings.Contains(got, "Verification surface") {
		t.Fatalf("goal = %q warnings = %q, want the drafted contract", got, warnings)
	}
}

// TestGoalDraftGraceBoundsTheWait pins the other bound: once the loop needs the
// contract it waits at most the grace, then cancels the draft with a deadline
// cause and carries the raw prompt with a timeout warning.
func TestGoalDraftGraceBoundsTheWait(t *testing.T) {
	defer func(g time.Duration) { goalDraftGrace = g }(goalDraftGrace)
	goalDraftGrace = 30 * time.Millisecond

	cause := make(chan error, 1)
	s := &Session{}
	p := s.startGoalDraft(context.Background(), rolemanager.NewPipeline(draftClassifier{delay: time.Minute, cause: cause}), "ship it")
	start := time.Now()
	var warnings []string
	got := s.joinGoalDraft(p, "ship it", func(e Event) { warnings = append(warnings, e.Warning) })
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("join waited %s, want about the grace", waited)
	}
	if got != "ship it" || len(warnings) != 1 || !strings.Contains(warnings[0], "timed out after") {
		t.Fatalf("goal = %q warnings = %q, want the raw prompt and a timeout warning", got, warnings)
	}
	select {
	case c := <-cause:
		if !errors.Is(c, context.DeadlineExceeded) {
			t.Fatalf("draft cancelled with %v, want a deadline cause", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the draft was not cancelled when the grace expired")
	}
}

// TestGoalDraftGraceCancelRecordsATimeout pins the feed row: a draft the loop
// stopped waiting for records verdict "timeout", not a generic error.
func TestGoalDraftGraceCancelRecordsATimeout(t *testing.T) {
	defer func(g time.Duration) { goalDraftGrace = g }(goalDraftGrace)
	goalDraftGrace = 20 * time.Millisecond

	verdicts := make(chan string, 4)
	cancel := rolemanager.SetObserver(func(a rolemanager.Activity) {
		if a.Event == rolemanager.EventGoalDraft {
			verdicts <- a.Verdict
		}
	})
	defer cancel()

	s := &Session{}
	p := s.startGoalDraft(context.Background(), rolemanager.NewPipeline(draftClassifier{delay: time.Minute}), "ship it")
	s.joinGoalDraft(p, "ship it", func(Event) {})
	select {
	case v := <-verdicts:
		if v != "timeout" {
			t.Fatalf("goal_draft verdict = %q, want timeout", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no goal_draft activity recorded")
	}
}
