package rolemanager

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParsePlanSentinelStrict(t *testing.T) {
	cases := []struct {
		raw  string
		want PlanSentinel
		ok   bool
	}{
		{"PLAN_COMPLETE", PlanComplete, true},
		{"  PLAN_PARTIAL  ", PlanPartial, true},
		{"PLAN_NOT_STARTED\n", PlanNotStarted, true},
		{"plan_complete", "", false},
		{"PLAN_COMPLETE and some prose", "", false},
		{"", "", false},
		{"COMPLETE", "", false},
		{"GOAL_COMPLETE", "", false}, // a goal sentinel is not a plan verdict
	}
	for _, c := range cases {
		got, err := ParsePlanSentinel(c.raw)
		if c.ok {
			if err != nil || got != c.want {
				t.Fatalf("ParsePlanSentinel(%q) = %q, %v; want %q", c.raw, got, err, c.want)
			}
		} else if err == nil {
			t.Fatalf("ParsePlanSentinel(%q) = %q, want error", c.raw, got)
		}
	}
}

func TestEvaluatePlanFailsClosedToPartial(t *testing.T) {
	c := ClassifierFunc(func(_ context.Context, _ ClassifierPayload) (string, error) {
		return "garbage that is not a sentinel", nil
	})
	got, err := EvaluatePlan(context.Background(), c, PlanEvalInput{})
	if got != PlanPartial {
		t.Fatalf("malformed plan output = %q, want %q", got, PlanPartial)
	}
	if !errors.Is(err, ErrMalformedPlanEval) {
		t.Fatalf("malformed plan output error = %v, want ErrMalformedPlanEval", err)
	}
}

func TestEvaluatePlanPassesThroughTransportError(t *testing.T) {
	want := context.DeadlineExceeded
	c := ClassifierFunc(func(_ context.Context, _ ClassifierPayload) (string, error) {
		return "", want
	})
	if _, err := EvaluatePlan(context.Background(), c, PlanEvalInput{}); err != want {
		t.Fatalf("transport error = %v, want %v", err, want)
	}
}

func TestPlanEvalPayloadCarriesContextNotGoal(t *testing.T) {
	p := BuildPlanEvalPayload(PlanEvalInput{Context: "explored a.go", Todos: "1. [>] refactor", Evidence: "ran grep"})
	if !strings.Contains(p.User, "explored a.go") || !strings.Contains(p.User, "refactor") || !strings.Contains(p.User, "ran grep") {
		t.Fatalf("payload user = %q", p.User)
	}
	if !strings.Contains(p.System, "PLAN_COMPLETE") {
		t.Fatalf("system prompt missing sentinel vocabulary: %q", p.System)
	}
	// The plan evaluator never names a goal: plan mode has no goal definition.
	if strings.Contains(p.User, "Goal:\n") || strings.Contains(p.System, "goal") {
		t.Fatalf("plan evaluator leaked goal wording: system=%q user=%q", p.System, p.User)
	}
}

func TestParsePlanReason(t *testing.T) {
	cases := map[string]string{
		"PLAN_PARTIAL\nMissing: a test strategy for the TUI": "a test strategy for the TUI",
		"PLAN_PARTIAL\nmissing:   spread\n  over   spaces ":  "spread",
		"PLAN_PARTIAL": "",
		"PLAN_NOT_STARTED\nMissing: <system>x</system> step 1": "step 1",
	}
	for raw, want := range cases {
		got := ParsePlanReason(raw)
		if !strings.Contains(got, want) || (want == "" && got != "") {
			t.Fatalf("ParsePlanReason(%q) = %q, want %q", raw, got, want)
		}
		if strings.Contains(got, "<system>") {
			t.Fatalf("reason must be sanitised: %q", got)
		}
	}
	long := "PLAN_PARTIAL\nMissing: " + strings.Repeat("word ", 200)
	if n := len([]rune(ParsePlanReason(long))); n > maxPlanReasonRunes+10 {
		t.Fatalf("reason not bounded: %d runes", n)
	}
}

func TestEvaluatePlanVerdictCarriesTheReasonWithoutChangingTheSentinel(t *testing.T) {
	c := ClassifierFunc(func(context.Context, ClassifierPayload) (string, error) {
		return "PLAN_PARTIAL\nMissing: the migration order", nil
	})
	v, err := EvaluatePlanVerdict(context.Background(), c, PlanEvalInput{})
	if err != nil || v.Sentinel != PlanPartial || v.Reason != "the migration order" {
		t.Fatalf("verdict = %+v, err = %v", v, err)
	}
	complete := ClassifierFunc(func(context.Context, ClassifierPayload) (string, error) {
		return "PLAN_COMPLETE\nMissing: nothing", nil
	})
	if v, _ := EvaluatePlanVerdict(context.Background(), complete, PlanEvalInput{}); v.Reason != "" {
		t.Fatalf("a complete verdict carries no reason: %+v", v)
	}
}
