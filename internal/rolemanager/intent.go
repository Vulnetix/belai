package rolemanager

import (
	"github.com/vulnetix/belai/internal/modes"
)

// Intent is the detected user intent for a turn. Intents map onto the three
// security modes (agent/plan/goal); handoff, debug and fan-out are agent-mode
// profiles selected by the intent detector.
type Intent string

const (
	IntentAgent   Intent = "agent"
	IntentPlan    Intent = "plan"
	IntentGoal    Intent = "goal"
	IntentHandoff Intent = "handoff"
	IntentDebug   Intent = "debug"
	IntentFanOut  Intent = "fanout"
)

// HandoffFacts holds harness-computed metadata about a plan-file attachment.
// It never carries the plan text; the text stays the classified attachment on
// the user turn.
type HandoffFacts struct {
	Label string
	Tasks int
	Paths []string
}

// ModeHint carries the currently selected mode and whether the user chose it
// explicitly (sticky). A sticky mode that disagrees with a confident detector
// triggers the deterministic mode-choice panel rather than being silently
// overridden.
type ModeHint struct {
	Mode   modes.Mode
	Sticky bool
}

// Decision maps an intent to a ModeDecision. Plan and goal reuse the existing
// carrier/explore wiring; handoff, debug and fan-out are agent mode with a
// builtin profile carrier. The caller fills in Scores and UserChosen.
func (i Intent) Decision(handoff *HandoffFacts) ModeDecision {
	switch i {
	case IntentPlan:
		return ModeDecision{Mode: modes.ModePlan, AppendCarrier: true, Explore: true, Intent: i}
	case IntentGoal:
		return ModeDecision{Mode: modes.ModeGoal, AppendCarrier: true, Explore: true, Intent: i}
	case IntentHandoff:
		return ModeDecision{
			Mode:          modes.ModeAgent,
			AgentName:     "belai:plan-handoff",
			AppendCarrier: true,
			Intent:        i,
			Handoff:       handoff,
		}
	case IntentDebug:
		return ModeDecision{Mode: modes.ModeAgent, AgentName: "belai:debug", AppendCarrier: true, Intent: i}
	case IntentFanOut:
		return ModeDecision{Mode: modes.ModeAgent, AgentName: "belai:fanout", AppendCarrier: true, Intent: i}
	default: // IntentAgent and any unknown intent
		return ModeDecision{Mode: modes.ModeAgent, Intent: i}
	}
}
