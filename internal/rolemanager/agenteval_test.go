package rolemanager

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParseAgentVerdictStrict(t *testing.T) {
	cases := []struct {
		raw  string
		want AgentVerdict
		ok   bool
	}{
		{"CONTINUE", AgentContinue, true},
		{"PAUSE", AgentPause, true},
		{" SLEEP ", AgentSleep, true},
		{"STOP", AgentStop, true},
		{"continue", "", false},
		{"CONTINUE now", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, err := ParseAgentVerdict(c.raw)
		if c.ok {
			if err != nil || got != c.want {
				t.Fatalf("ParseAgentVerdict(%q) = %q, %v; want %q", c.raw, got, err, c.want)
			}
		} else if err == nil {
			t.Fatalf("ParseAgentVerdict(%q) = %q, want error", c.raw, got)
		}
	}
}

func TestEvaluateAgentFailsClosedToPause(t *testing.T) {
	c := ClassifierFunc(func(_ context.Context, _ ClassifierPayload) (string, error) {
		return "ramble", nil
	})
	got, err := EvaluateAgent(context.Background(), c, "goals", "output")
	if got != AgentPause {
		t.Fatalf("malformed agent output = %q, want %q", got, AgentPause)
	}
	if !errors.Is(err, ErrMalformedAgentEval) {
		t.Fatalf("malformed agent output error = %v, want ErrMalformedAgentEval", err)
	}
}

func TestEvaluateAgentPassesThroughTransportError(t *testing.T) {
	want := context.DeadlineExceeded
	c := ClassifierFunc(func(_ context.Context, _ ClassifierPayload) (string, error) {
		return "", want
	})
	if _, err := EvaluateAgent(context.Background(), c, "goals", "out"); err != want {
		t.Fatalf("transport error = %v, want %v", err, want)
	}
}

func TestAgentEvalPayloadCarriesGoalsAndOutput(t *testing.T) {
	p := BuildAgentEvalPayload("keep the build green", "CI passed")
	if !strings.Contains(p.User, "keep the build green") || !strings.Contains(p.User, "CI passed") {
		t.Fatalf("payload user = %q", p.User)
	}
	if !strings.Contains(p.System, "CONTINUE") {
		t.Fatalf("system prompt missing verdict vocabulary: %q", p.System)
	}
}
