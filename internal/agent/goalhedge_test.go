package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/rolemanager"
)

// slowDecider answers after a delay: with fixed scores, or with an outage.
type slowDecider struct {
	goalDecider
	delay time.Duration
	fail  bool
}

func (d *slowDecider) Decide(ctx context.Context, r decisions.Request) (decisions.Result, error) {
	time.Sleep(d.delay)
	if d.fail {
		return decisions.Result{}, &decisions.Error{Class: decisions.ClassUnavailable, Msg: "down", Err: errors.New("503")}
	}
	return d.goalDecider.Decide(ctx, r)
}

func withHedge(t *testing.T, d time.Duration) {
	t.Helper()
	old := goalJudgeHedge
	goalJudgeHedge = d
	t.Cleanup(func() { goalJudgeHedge = old })
}

// A backend slower than the hedge that then fails no longer costs its whole
// delay before the model is asked: the model judge started at the hedge, and
// its one answer stands.
func TestASlowFailingBackendDoesNotHoldTheModelJudge(t *testing.T) {
	withHedge(t, 10*time.Millisecond)
	calls := &judgeCalls{}
	s := goalJudgeSession(t, &slowDecider{delay: 150 * time.Millisecond, fail: true})
	l := passLedger{goalText: "add a flag"}
	got, _, err := s.evaluateGoalPass(context.Background(), calls.pipe("GOAL_COMPLETE"), &l, "evidence", func(Event) {})
	if err != nil || got != rolemanager.GoalComplete || len(calls.payloads) != 1 {
		t.Fatalf("got %q err=%v model calls=%d", got, err, len(calls.payloads))
	}
}

// A slow backend that is unsure still hands its scores to the model judge:
// the early judge is cancelled and the scored one answers.
func TestASlowUnsureBackendStillHandsOverItsScores(t *testing.T) {
	withHedge(t, 10*time.Millisecond)
	calls := &judgeCalls{}
	s := goalJudgeSession(t, &slowDecider{goalDecider: goalDecider{complete: 0.6, partial: 0.5, notStarted: 0.05}, delay: 100 * time.Millisecond})
	l := passLedger{goalText: "add a flag"}
	got, _, _ := s.evaluateGoalPass(context.Background(), calls.pipe("GOAL_COMPLETE"), &l, "evidence", func(Event) {})
	if got != rolemanager.GoalComplete {
		t.Fatalf("got %q", got)
	}
	calls.mu.Lock()
	defer calls.mu.Unlock()
	last := calls.payloads[len(calls.payloads)-1]
	if !strings.Contains(last.User, "complete: 60%") {
		t.Fatalf("the answering judge did not see the scores:\n%s", last.User)
	}
}

// A slow backend that settles the pass wins over the judge started early.
func TestASlowClearBackendStillDecides(t *testing.T) {
	withHedge(t, 10*time.Millisecond)
	calls := &judgeCalls{}
	s := goalJudgeSession(t, &slowDecider{goalDecider: goalDecider{complete: 0.01, partial: 0.05, notStarted: 0.96}, delay: 100 * time.Millisecond})
	l := passLedger{goalText: "add a flag"}
	if got, _, _ := s.evaluateGoalPass(context.Background(), calls.pipe("GOAL_COMPLETE"), &l, "evidence", func(Event) {}); got != rolemanager.GoalNotStarted {
		t.Fatalf("got %q, want the backend's clear verdict", got)
	}
}
