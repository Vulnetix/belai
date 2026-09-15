// Package agent implements the tool-execution loop and carrier resolution.
package agent

import (
	"fmt"
	"os"

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
func CarrierOptions(workdir string, d rolemanager.ModeDecision, st config.State, set config.Settings) (prompt.Options, error) {
	var planText, goalText, profileText string
	var loadErr error

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
			if err == nil {
				profileText = prof.Content
			} else {
				loadErr = fmt.Errorf("load profile %q: %w", d.AgentName, err)
				fmt.Fprintf(os.Stderr, "signet: warning: %v; falling back to default agent\n", loadErr)
			}
		}
	}

	opts := d.PromptOptions(planText, goalText, profileText)
	opts.Caveman = set.Caveman != nil && *set.Caveman

	if loadErr != nil {
		return prompt.Options{}, nil
	}
	return opts, nil
}
