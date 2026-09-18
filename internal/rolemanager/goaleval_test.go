package rolemanager

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParseGoalSentinelStrict(t *testing.T) {
	cases := []struct {
		raw  string
		want GoalSentinel
		ok   bool
	}{
		{"GOAL_COMPLETE", GoalComplete, true},
		{"  GOAL_PARTIAL  ", GoalPartial, true},
		{"GOAL_NOT_STARTED\n", GoalNotStarted, true},
		{"GOAL_COMPLETE.", GoalComplete, true},
		{"**GOAL_PARTIAL**", GoalPartial, true},
		{"<thinking>…</thinking>\nGOAL_COMPLETE", GoalComplete, true},
		{"goal_complete", "", false},
		{"GOAL_COMPLETE and some prose", "", false},
		{"", "", false},
		{"COMPLETE", "", false},
	}
	for _, c := range cases {
		got, err := ParseGoalSentinel(c.raw)
		if c.ok {
			if err != nil || got != c.want {
				t.Fatalf("ParseGoalSentinel(%q) = %q, %v; want %q", c.raw, got, err, c.want)
			}
		} else if err == nil {
			t.Fatalf("ParseGoalSentinel(%q) = %q, want error", c.raw, got)
		}
	}
}

func TestEvaluateGoalFailsClosedToPartial(t *testing.T) {
	c := ClassifierFunc(func(_ context.Context, _ ClassifierPayload) (string, error) {
		return "garbage that is not a sentinel", nil
	})
	got, err := EvaluateGoal(context.Background(), c, GoalEvalInput{})
	if got != GoalPartial {
		t.Fatalf("malformed goal output = %q, want %q", got, GoalPartial)
	}
	if !errors.Is(err, ErrMalformedGoalEval) {
		t.Fatalf("malformed goal output error = %v, want ErrMalformedGoalEval", err)
	}
}

func TestEvaluateGoalPassesThroughTransportError(t *testing.T) {
	want := context.DeadlineExceeded
	c := ClassifierFunc(func(_ context.Context, _ ClassifierPayload) (string, error) {
		return "", want
	})
	if _, err := EvaluateGoal(context.Background(), c, GoalEvalInput{}); err != want {
		t.Fatalf("transport error = %v, want %v", err, want)
	}
}

func TestGoalEvalPayloadCarriesEvidence(t *testing.T) {
	p := BuildGoalEvalPayload(GoalEvalInput{Goal: "ship", Todos: "1. [>] build", Evidence: "ran grep"})
	if !strings.Contains(p.User, "ship") || !strings.Contains(p.User, "build") || !strings.Contains(p.User, "ran grep") {
		t.Fatalf("payload user = %q", p.User)
	}
	if !strings.Contains(p.System, "GOAL_COMPLETE") {
		t.Fatalf("system prompt missing sentinel vocabulary: %q", p.System)
	}
}
