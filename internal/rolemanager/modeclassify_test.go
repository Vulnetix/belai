package rolemanager

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParseModeSentinelValid(t *testing.T) {
	cases := []struct {
		in   string
		want ModeSentinel
	}{
		{"AGENT", ModeAgent},
		{"  PLAN\n", ModePlan},
		{"GOAL", ModeGoal},
		{"UNDETERMINED", ModeUndetermined},
	}
	for _, tc := range cases {
		got, err := ParseModeSentinel(tc.in)
		if err != nil {
			t.Fatalf("ParseModeSentinel(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("ParseModeSentinel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseModeSentinelRejectsMalformed(t *testing.T) {
	for _, in := range []string{"", "agent", "AGENT.", "plan goal", "SAFE"} {
		if _, err := ParseModeSentinel(in); err == nil {
			t.Fatalf("ParseModeSentinel(%q) expected error", in)
		}
	}
}

func TestBuildModeClassifierPayloadIsPromptOnly(t *testing.T) {
	p := BuildModeClassifierPayload("write a plan for the widget")
	if p.System == "" {
		t.Fatalf("mode classifier payload must carry a system prompt")
	}
	if !strings.Contains(p.System, "mode") {
		t.Fatalf("system prompt should be the prompt-classifier prompt: %q", p.System)
	}
	if p.User != "write a plan for the widget" {
		t.Fatalf("user content = %q", p.User)
	}
	if len(p.Tools) != 0 || len(p.Skills) != 0 || p.Agent != "" {
		t.Fatalf("mode classifier payload must carry no tools/skills/agent: %+v", p)
	}
}

func TestClassifyMode(t *testing.T) {
	fc := &fakeClassifier{raw: "GOAL"}
	got, err := ClassifyMode(context.Background(), fc, "ship the thing")
	if err != nil {
		t.Fatalf("ClassifyMode: %v", err)
	}
	if got != ModeGoal {
		t.Fatalf("ClassifyMode = %q, want GOAL", got)
	}
	if fc.payload.User != "ship the thing" {
		t.Fatalf("classifier received %q", fc.payload.User)
	}
}

func TestClassifyModeMalformedFailsClosed(t *testing.T) {
	fc := &fakeClassifier{raw: "not a mode"}
	got, err := ClassifyMode(context.Background(), fc, "hello")
	if err != nil {
		t.Fatalf("malformed output should fail closed without error, got %v", err)
	}
	if got != ModeUndetermined {
		t.Fatalf("ClassifyMode = %q, want UNDETERMINED", got)
	}
}

func TestClassifyModePropagatesError(t *testing.T) {
	fc := &fakeClassifier{err: errors.New("down")}
	if _, err := ClassifyMode(context.Background(), fc, "hello"); err == nil {
		t.Fatalf("expected classifier error to propagate")
	}
}

func TestExtractAgentName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"use @agent:security-expert to review this", "security-expert"},
		{"@agent:go-expert", "go-expert"},
		{"no agent here", ""},
	}
	for _, tc := range cases {
		if got := ExtractAgentName(tc.in); got != tc.want {
			t.Fatalf("ExtractAgentName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
