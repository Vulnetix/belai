package rolemanager

import (
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/modes"
)

func TestDecideForcedModeEngagesTheChosenMode(t *testing.T) {
	cases := []struct {
		name    string
		mode    modes.Mode
		want    modes.Mode
		carrier bool
	}{
		{"goal", modes.ModeGoal, modes.ModeGoal, true},
		{"plan", modes.ModePlan, modes.ModePlan, true},
		{"agent", modes.ModeAgent, modes.ModeAgent, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := DecideForcedMode(c.mode, "do the thing", false)
			if d.Mode != c.want {
				t.Fatalf("mode = %q, want %q", d.Mode, c.want)
			}
			if d.AppendCarrier != c.carrier {
				t.Fatalf("AppendCarrier = %v, want %v", d.AppendCarrier, c.carrier)
			}
		})
	}
}

// The goal length limit guards against the *classifier* misrouting a long
// prompt. A user who selected goal mode has made no such mistake, so an
// explicit choice must survive a prompt longer than the limit.
func TestDecideForcedGoalIgnoresTheLengthLimit(t *testing.T) {
	long := strings.Repeat("x", DefaultGoalPromptLengthLimit+1)

	if d := DecideMode(ModeGoal, ModeInput{Prompt: long}); d.Mode != modes.ModeAgent || d.Warning == "" {
		t.Fatalf("classifier path should demote to agent with a warning, got %+v", d)
	}

	d := DecideForcedMode(modes.ModeGoal, long, false)
	if d.Mode != modes.ModeGoal {
		t.Fatalf("forced mode = %q, want goal", d.Mode)
	}
	if d.Warning != "" {
		t.Fatalf("forced goal should not warn, got %q", d.Warning)
	}
}

func TestDecideForcedGoalExploresOnlyWithReferences(t *testing.T) {
	if d := DecideForcedMode(modes.ModeGoal, "ship it", false); d.Explore {
		t.Fatal("goal with no references should pursue immediately")
	}
	if d := DecideForcedMode(modes.ModeGoal, "ship it", true); !d.Explore {
		t.Fatal("goal with references should explore first")
	}
}

func TestDecideForcedModeUnknownFallsBackToAgent(t *testing.T) {
	d := DecideForcedMode(modes.Mode("nonsense"), "hello", false)
	if d.Mode != modes.ModeAgent || d.AppendCarrier {
		t.Fatalf("want bare agent mode, got %+v", d)
	}
}

// A named agent reference still engages that profile under an explicit choice.
func TestDecideForcedAgentKeepsNamedAgent(t *testing.T) {
	d := DecideForcedMode(modes.ModeAgent, "ask @agent:review-bot to look", false)
	if d.AgentName != "review-bot" || !d.AppendCarrier {
		t.Fatalf("want named agent carrier, got %+v", d)
	}
}
