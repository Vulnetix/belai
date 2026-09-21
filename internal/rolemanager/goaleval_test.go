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

// A malformed reply is re-asked once with the exact syntax it broke, and a
// repaired reply is a clean verdict — not a malformed one.
func TestEvaluateGoalRepairsMalformedReply(t *testing.T) {
	var calls int
	var repairUser string
	c := ClassifierFunc(func(_ context.Context, p ClassifierPayload) (string, error) {
		calls++
		if calls == 1 {
			return "Answer: the goal looks partially done", nil
		}
		repairUser = p.User
		return "GOAL_PARTIAL", nil
	})
	got, err := EvaluateGoal(context.Background(), c, GoalEvalInput{Goal: "ship"})
	if err != nil {
		t.Fatalf("a repaired reply must not report an error: %v", err)
	}
	if got != GoalPartial {
		t.Fatalf("verdict = %q, want %q", got, GoalPartial)
	}
	if calls != 2 {
		t.Fatalf("classifier calls = %d, want 2 (first reply, then one repair round)", calls)
	}
	for _, want := range []string{"Answer: the goal looks partially done", string(GoalComplete), string(GoalPartial), string(GoalNotStarted)} {
		if !strings.Contains(repairUser, want) {
			t.Fatalf("repair payload missing %q:\n%s", want, repairUser)
		}
	}
}

// Two malformed replies in a row still fail closed to GOAL_PARTIAL with
// ErrMalformedGoalEval, so the pass loop can count the failure.
func TestEvaluateGoalRepairRoundStillMalformed(t *testing.T) {
	var calls int
	c := ClassifierFunc(func(_ context.Context, _ ClassifierPayload) (string, error) {
		calls++
		return "still not a token", nil
	})
	got, err := EvaluateGoal(context.Background(), c, GoalEvalInput{})
	if got != GoalPartial || !errors.Is(err, ErrMalformedGoalEval) {
		t.Fatalf("got (%q, %v), want (%q, ErrMalformedGoalEval)", got, err, GoalPartial)
	}
	if calls != 2 {
		t.Fatalf("classifier calls = %d, want 2", calls)
	}
}

// A transport failure on the repair round leaves the verdict unknown, which
// fails closed rather than surfacing as a transport error: the first reply
// was already unusable.
func TestEvaluateGoalRepairTransportFailureFailsClosed(t *testing.T) {
	var calls int
	c := ClassifierFunc(func(_ context.Context, _ ClassifierPayload) (string, error) {
		calls++
		if calls == 1 {
			return "nonsense", nil
		}
		return "", context.DeadlineExceeded
	})
	got, err := EvaluateGoal(context.Background(), c, GoalEvalInput{})
	if got != GoalPartial || !errors.Is(err, ErrMalformedGoalEval) {
		t.Fatalf("got (%q, %v), want (%q, ErrMalformedGoalEval)", got, err, GoalPartial)
	}
}

// The harness facts block is rendered as its own labelled section, apart from
// the untrusted digest.
func TestGoalEvalPayloadCarriesHarnessFacts(t *testing.T) {
	p := BuildGoalEvalPayload(GoalEvalInput{Goal: "ship", Facts: "Files changed this pass: 2\n", Evidence: "ran grep"})
	if !strings.Contains(p.User, "Harness-observed facts:") || !strings.Contains(p.User, "Files changed this pass: 2") {
		t.Fatalf("payload user missing the facts block:\n%s", p.User)
	}
	if strings.Index(p.User, "Harness-observed facts:") > strings.Index(p.User, "Pass evidence digest:") {
		t.Fatalf("facts must precede the untrusted digest:\n%s", p.User)
	}
}

// An unbounded pass digest is what makes an evaluator answer with something
// other than one token. The tail is kept, because the end of a pass is where
// its outcome is.
func TestGoalEvalPayloadTruncatesEvidence(t *testing.T) {
	long := strings.Repeat("a", MaxGoalEvidenceChars) + "TAIL"
	p := BuildGoalEvalPayload(GoalEvalInput{Goal: "ship", Evidence: long})
	if len(p.User) > MaxGoalEvidenceChars+500 {
		t.Fatalf("payload user = %d chars, want the evidence bounded", len(p.User))
	}
	if !strings.Contains(p.User, "TAIL") {
		t.Fatal("truncation must keep the tail of the digest")
	}
	if !strings.Contains(p.User, "earlier evidence omitted") {
		t.Fatal("truncation must mark the cut")
	}
}

// Every classifier payload is tool-less, and the repair payload is no
// exception.
func TestGoalEvalRepairPayloadCarriesNoTools(t *testing.T) {
	p := BuildGoalEvalRepairPayload(GoalEvalInput{Goal: "ship"}, "nonsense")
	if len(p.Tools) != 0 || len(p.Skills) != 0 || p.Agent != "" {
		t.Fatalf("repair payload must carry no tools, skills or agent block: %+v", p)
	}
}
