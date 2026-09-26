package rolemanager

import (
	"testing"

	"github.com/vulnetix/belai/internal/modes"
)

func TestIntentDecision(t *testing.T) {
	cases := []struct {
		intent  Intent
		handoff *HandoffFacts
		want    ModeDecision
	}{
		{IntentAgent, nil, ModeDecision{Mode: modes.ModeAgent, Intent: IntentAgent}},
		{IntentPlan, nil, ModeDecision{Mode: modes.ModePlan, AppendCarrier: true, Explore: true, Intent: IntentPlan}},
		{IntentGoal, nil, ModeDecision{Mode: modes.ModeGoal, AppendCarrier: true, Explore: true, Intent: IntentGoal}},
		{IntentHandoff, &HandoffFacts{Tasks: 3}, ModeDecision{Mode: modes.ModeAgent, AgentName: "belai:plan-handoff", AppendCarrier: true, Intent: IntentHandoff, Handoff: &HandoffFacts{Tasks: 3}}},
		{IntentDebug, nil, ModeDecision{Mode: modes.ModeAgent, AgentName: "belai:debug", AppendCarrier: true, Intent: IntentDebug}},
		{IntentFanOut, nil, ModeDecision{Mode: modes.ModeAgent, AgentName: "belai:fanout", AppendCarrier: true, Intent: IntentFanOut}},
		{Intent("unknown"), nil, ModeDecision{Mode: modes.ModeAgent, Intent: Intent("unknown")}},
	}
	for _, c := range cases {
		got := c.intent.Decision(c.handoff)
		if !modeDecisionsEqual(got, c.want) {
			t.Errorf("%q.Decision(%v) = %+v, want %+v", c.intent, c.handoff, got, c.want)
		}
	}
}

func modeDecisionsEqual(a, b ModeDecision) bool {
	if a.Mode != b.Mode || a.AgentName != b.AgentName || a.Warning != b.Warning || a.AppendCarrier != b.AppendCarrier || a.Explore != b.Explore || a.Intent != b.Intent {
		return false
	}
	if (a.Handoff == nil) != (b.Handoff == nil) {
		return false
	}
	if a.Handoff != nil && (a.Handoff.Label != b.Handoff.Label || a.Handoff.Tasks != b.Handoff.Tasks || len(a.Handoff.Paths) != len(b.Handoff.Paths)) {
		return false
	}
	return true
}
