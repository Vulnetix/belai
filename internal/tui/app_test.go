package tui

import (
	"testing"

	"github.com/vulnetix/signet/internal/rolemanager"
)

func TestNewAppView(t *testing.T) {
	a := NewApp(t.TempDir(), "")
	if a == nil {
		t.Fatalf("NewApp returned nil")
	}
	if v := a.View(); v == "" {
		t.Fatalf("View returned empty string")
	}
}

type fakeClassifier struct {
	raw string
}

func (f fakeClassifier) Classify(rolemanager.ClassifierPayload) (string, error) {
	return f.raw, nil
}

func TestClassifyModeSelectsPlan(t *testing.T) {
	a := NewApp(t.TempDir(), "")
	a.SetClassifier(fakeClassifier{raw: "PLAN"})

	a.classifyMode("figure out how to refactor this")

	if a.mode != "plan" {
		t.Fatalf("mode = %q, want plan", a.mode)
	}
	if len(a.messages) == 0 {
		t.Fatalf("expected a mode message")
	}
}

func TestClassifyModeGoalOverLimitDefaultsToAgent(t *testing.T) {
	a := NewApp(t.TempDir(), "")
	a.SetClassifier(fakeClassifier{raw: "GOAL"})

	long := make([]byte, rolemanager.DefaultGoalPromptLengthLimit+1)
	for i := range long {
		long[i] = 'x'
	}
	a.classifyMode(string(long))

	if a.mode != "agent" {
		t.Fatalf("mode = %q, want agent (goal length exceeded)", a.mode)
	}
	if a.modeWarning == "" {
		t.Fatalf("expected a length warning")
	}
}

func TestClassifyModeNamedAgent(t *testing.T) {
	a := NewApp(t.TempDir(), "")
	a.SetClassifier(fakeClassifier{raw: "AGENT"})

	a.classifyMode("review this @agent:security-expert")

	if a.mode != "agent" {
		t.Fatalf("mode = %q, want agent", a.mode)
	}
	if a.namedAgent != "security-expert" {
		t.Fatalf("namedAgent = %q, want security-expert", a.namedAgent)
	}
}

func TestClassifyModeSkippedWithoutClassifier(t *testing.T) {
	a := NewApp(t.TempDir(), "")
	a.classifyMode("anything")
	if a.mode != "agent" {
		t.Fatalf("mode = %q, want unchanged agent", a.mode)
	}
	if len(a.messages) != 0 {
		t.Fatalf("no classifier installed, expected no messages, got %d", len(a.messages))
	}
}
