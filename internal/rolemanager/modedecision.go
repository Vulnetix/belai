package rolemanager

import (
	"context"
	"fmt"
	"unicode/utf8"

	"github.com/vulnetix/signet/internal/modes"
	"github.com/vulnetix/signet/internal/prompt"
)

// DefaultGoalPromptLengthLimit is the default maximum prompt length (runes)
// allowed for goal mode. When a goal-classified prompt exceeds it, the harness
// warns and engages default agent mode instead.
const DefaultGoalPromptLengthLimit = 4000

// ModeInput is the raw input to operating-mode selection.
type ModeInput struct {
	// Prompt is the user prompt, with no attachment contents or referenced
	// files (the classifier sees only this).
	Prompt string
	// GoalLimit is the goal-mode length limit in runes; <= 0 uses the default.
	GoalLimit int
	// HasReferences reports whether attachments or referenced files were
	// provided alongside the prompt.
	HasReferences bool
}

// ModeDecision is the outcome of operating-mode selection.
type ModeDecision struct {
	// Mode is the engaged mode (agent is the default/fallback).
	Mode modes.Mode
	// AgentName is the named agent to engage (agent mode only).
	AgentName string
	// Warning, when non-empty, is surfaced to the user (e.g. goal length
	// exceeded).
	Warning string
	// AppendCarrier is true when the system prompt should carry the active
	// plan, goal, or agent profile.
	AppendCarrier bool
	// Explore is true when the engaged mode should launch explore agent(s).
	Explore bool
}

// DecideMode maps a classifier sentinel plus prompt metadata to a decision.
func DecideMode(s ModeSentinel, in ModeInput) ModeDecision {
	limit := in.GoalLimit
	if limit <= 0 {
		limit = DefaultGoalPromptLengthLimit
	}
	length := utf8.RuneCountInString(in.Prompt)
	agentName := ExtractAgentName(in.Prompt)

	switch s {
	case ModeGoal:
		if length > limit {
			record(EventModeGoalLengthLimit, string(ModeAgent), "", fmt.Sprintf("len=%d limit=%d", length, limit), 0)
			return ModeDecision{
				Mode:    modes.ModeAgent,
				Warning: fmt.Sprintf("prompt exceeds the goal mode length limit (%d); engaging default agent mode", limit),
			}
		}
		// Pursue immediately when no references/attachments are present;
		// otherwise explore first.
		return ModeDecision{Mode: modes.ModeGoal, AppendCarrier: true, Explore: in.HasReferences}
	case ModePlan:
		return ModeDecision{Mode: modes.ModePlan, AppendCarrier: true, Explore: true}
	case ModeAgent:
		if agentName != "" {
			return ModeDecision{Mode: modes.ModeAgent, AgentName: agentName, AppendCarrier: true}
		}
		return ModeDecision{Mode: modes.ModeAgent}
	default: // ModeUndetermined (and any unparseable value) -> default agent
		return ModeDecision{Mode: modes.ModeAgent}
	}
}

// DecideForcedMode builds the decision for a mode the user chose explicitly,
// bypassing the classifier. It reuses DecideMode so an explicit choice gets the
// same carrier and explore wiring as an inferred one — with one deliberate
// difference: the goal length limit does not apply. The limit exists to stop
// the classifier from routing a long prompt into goal mode by mistake, and a
// user who selected goal mode has made no mistake to guard against.
func DecideForcedMode(m modes.Mode, prompt string, hasReferences bool) ModeDecision {
	record(EventModeForced, string(m), "", "", 0)
	in := ModeInput{Prompt: prompt, HasReferences: hasReferences}
	switch m {
	case modes.ModeGoal:
		return ModeDecision{Mode: modes.ModeGoal, AppendCarrier: true, Explore: hasReferences}
	case modes.ModePlan:
		return DecideMode(ModePlan, in)
	case modes.ModeAgent:
		return DecideMode(ModeAgent, in)
	default:
		return DecideMode(ModeUndetermined, in)
	}
}

// Select runs the mode classifier and returns the resulting decision. A
// classifier transport error is returned to the caller; malformed classifier
// output falls through to default agent mode via ClassifyMode.
func Select(ctx context.Context, c Classifier, in ModeInput) (ModeDecision, error) {
	s, err := ClassifyMode(ctx, c, in.Prompt)
	if err != nil {
		return ModeDecision{}, err
	}
	return DecideMode(s, in), nil
}

// PromptOptions maps the decision to the system-prompt carrier. Default agent
// mode (no carrier) yields an empty Options, so only the base signet system
// prompt is used.
func (d ModeDecision) PromptOptions(planText, goalText, profileText string) prompt.Options {
	opts := prompt.Options{}
	if !d.AppendCarrier {
		return opts
	}
	switch d.Mode {
	case modes.ModePlan:
		opts.Carrier = prompt.CarrierPlan
		opts.PlanText = planText
	case modes.ModeGoal:
		opts.Carrier = prompt.CarrierGoal
		opts.GoalText = goalText
	case modes.ModeAgent:
		opts.Carrier = prompt.CarrierProfile
		opts.ProfileText = profileText
	}
	return opts
}
