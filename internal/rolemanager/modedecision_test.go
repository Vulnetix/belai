package rolemanager

import (
	"context"
	"errors"
	"testing"

	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/prompt"
)

func TestDecideMode(t *testing.T) {
	long := make([]byte, DefaultGoalPromptLengthLimit+1)
	for i := range long {
		long[i] = 'x'
	}
	longPrompt := string(long)

	cases := []struct {
		name     string
		sentinel ModeSentinel
		in       ModeInput
		want     ModeDecision
	}{
		{
			name:     "goal within limit no references pursues immediately",
			sentinel: ModeGoal,
			in:       ModeInput{Prompt: "ship the belai release"},
			want:     ModeDecision{Mode: modes.ModeGoal, AppendCarrier: true, Explore: false},
		},
		{
			name:     "goal within limit with references explores",
			sentinel: ModeGoal,
			in:       ModeInput{Prompt: "ship it", HasReferences: true},
			want:     ModeDecision{Mode: modes.ModeGoal, AppendCarrier: true, Explore: true},
		},
		{
			name:     "goal over limit warns and defaults to agent",
			sentinel: ModeGoal,
			in:       ModeInput{Prompt: longPrompt},
			want:     ModeDecision{Mode: modes.ModeAgent, Warning: "prompt exceeds the goal mode length limit"},
		},
		{
			name:     "plan engages plan mode and explores",
			sentinel: ModePlan,
			in:       ModeInput{Prompt: "figure out how to refactor"},
			want:     ModeDecision{Mode: modes.ModePlan, AppendCarrier: true, Explore: true},
		},
		{
			name:     "agent without name is default agent",
			sentinel: ModeAgent,
			in:       ModeInput{Prompt: "add a test"},
			want:     ModeDecision{Mode: modes.ModeAgent},
		},
		{
			name:     "agent with name engages named agent",
			sentinel: ModeAgent,
			in:       ModeInput{Prompt: "review @agent:security-expert"},
			want:     ModeDecision{Mode: modes.ModeAgent, AgentName: "security-expert", AppendCarrier: true},
		},
		{
			name:     "undetermined is default agent",
			sentinel: ModeUndetermined,
			in:       ModeInput{Prompt: "whatever"},
			want:     ModeDecision{Mode: modes.ModeAgent},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DecideMode(tc.sentinel, tc.in)
			if got.Mode != tc.want.Mode {
				t.Fatalf("Mode = %q, want %q", got.Mode, tc.want.Mode)
			}
			if got.AgentName != tc.want.AgentName {
				t.Fatalf("AgentName = %q, want %q", got.AgentName, tc.want.AgentName)
			}
			if got.AppendCarrier != tc.want.AppendCarrier {
				t.Fatalf("AppendCarrier = %v, want %v", got.AppendCarrier, tc.want.AppendCarrier)
			}
			if got.Explore != tc.want.Explore {
				t.Fatalf("Explore = %v, want %v", got.Explore, tc.want.Explore)
			}
			if tc.want.Warning != "" && got.Warning == "" {
				t.Fatalf("expected warning, got none")
			}
			if tc.want.Warning == "" && got.Warning != "" {
				t.Fatalf("unexpected warning: %q", got.Warning)
			}
		})
	}
}

func TestSelect(t *testing.T) {
	fc := &fakeClassifier{raw: "PLAN"}
	d, err := Select(context.Background(), fc, ModeInput{Prompt: "plan the migration"})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if d.Mode != modes.ModePlan || !d.AppendCarrier || !d.Explore {
		t.Fatalf("Select = %+v", d)
	}

	// classifier transport error propagates
	if _, err := Select(context.Background(), &fakeClassifier{err: errors.New("down")}, ModeInput{Prompt: "x"}); err == nil {
		t.Fatalf("expected classifier error to propagate")
	}

	// malformed output falls through to default agent mode
	d2, err := Select(context.Background(), &fakeClassifier{raw: "garbage"}, ModeInput{Prompt: "x"})
	if err != nil {
		t.Fatalf("Select malformed: %v", err)
	}
	if d2.Mode != modes.ModeAgent || d2.AppendCarrier {
		t.Fatalf("malformed Select = %+v, want default agent", d2)
	}
}

func TestModeDecisionPromptOptions(t *testing.T) {
	// default agent: no carrier
	d := ModeDecision{Mode: modes.ModeAgent}
	opts := d.PromptOptions("plan", "goal", "profile")
	if opts.Carrier != prompt.CarrierNone {
		t.Fatalf("default agent carrier = %q, want none", opts.Carrier)
	}

	// named agent: profile carrier
	d = ModeDecision{Mode: modes.ModeAgent, AgentName: "security-expert", AppendCarrier: true}
	opts = d.PromptOptions("", "", "expert profile")
	if opts.Carrier != prompt.CarrierProfile || opts.ProfileText != "expert profile" {
		t.Fatalf("named agent opts = %+v", opts)
	}

	// goal: goal carrier
	d = ModeDecision{Mode: modes.ModeGoal, AppendCarrier: true}
	opts = d.PromptOptions("", "my goal", "")
	if opts.Carrier != prompt.CarrierGoal || opts.GoalText != "my goal" {
		t.Fatalf("goal opts = %+v", opts)
	}

	// plan: plan carrier
	d = ModeDecision{Mode: modes.ModePlan, AppendCarrier: true}
	opts = d.PromptOptions("my plan", "", "")
	if opts.Carrier != prompt.CarrierPlan || opts.PlanText != "my plan" {
		t.Fatalf("plan opts = %+v", opts)
	}
}
