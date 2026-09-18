// Package agent implements the tool-execution loop and carrier resolution.
package agent

import (
	"fmt"
	"os"

	"github.com/vulnetix/signet/internal/agentprofile"
	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/goals"
	"github.com/vulnetix/signet/internal/modes"
	"github.com/vulnetix/signet/internal/plans"
	"github.com/vulnetix/signet/internal/profiles"
	"github.com/vulnetix/signet/internal/prompt"
	"github.com/vulnetix/signet/internal/rolemanager"
)

// CarrierOptions resolves the active plan, goal, or agent profile and maps
// the mode decision to prompt options. On any load failure it returns a bare
// prompt.Options so that a missing carrier file never hard-errors a session.
//
// When executePlan is true the named plan is loaded as the carrier regardless
// of d.Mode, so an approved plan can be executed in the same session without
// waiting for state persistence to propagate.
func CarrierOptions(workdir string, d rolemanager.ModeDecision, executePlan bool, planName string, st config.State, set config.Settings) (prompt.Options, error) {
	var planText, goalText, profileText string
	var loadErr error

	if executePlan && planName != "" {
		p, err := plans.Load(workdir, planName)
		if err == nil {
			planText = p.Content
		} else {
			loadErr = fmt.Errorf("load plan %q: %w", planName, err)
		}
	}

	switch d.Mode {
	case modes.ModeGoal:
		g, err := goals.Load(workdir, st.ActiveGoal)
		if err == nil {
			goalText = g.Content
		} else {
			loadErr = fmt.Errorf("load goal %q: %w", st.ActiveGoal, err)
		}
	case modes.ModePlan:
		p, err := plans.Load(workdir, st.ActivePlan)
		if err == nil {
			planText = p.Content
		} else {
			loadErr = fmt.Errorf("load plan %q: %w", st.ActivePlan, err)
		}
	case modes.ModeAgent:
		if d.AgentName != "" {
			prof, err := profiles.Load(d.AgentName)
			switch {
			case err == nil:
				profileText = prof.Content
			default:
				// A background-agent definition can carry a foreground turn
				// too: its system_prompt is the same kind of text, stored in
				// the other tree. Flat profiles win the name.
				if bg, bgErr := agentprofile.Load(d.AgentName); bgErr == nil {
					profileText = bg.SystemPrompt
					break
				}
				loadErr = fmt.Errorf("load profile %q: %w", d.AgentName, err)
				fmt.Fprintf(os.Stderr, "signet: warning: %v; falling back to default agent\n", loadErr)
			}
		}
	}

	opts := d.PromptOptions(planText, goalText, profileText)
	if executePlan && planName != "" {
		// Executing an approved plan runs with the full agent-mode tool surface
		// but carries the approved plan text in the system prompt.
		opts.Carrier = prompt.CarrierPlan
		opts.PlanText = planText
	}
	opts.Caveman = set.Caveman != nil && *set.Caveman

	if loadErr != nil {
		return prompt.Options{}, nil
	}
	return opts, nil
}
