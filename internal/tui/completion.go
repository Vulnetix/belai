package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/vulnetix/signet/internal/modes"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/tui/components"
)

// completionRole is the transcript role of the agent-mode completion panel.
// It is render-only: buildTurns never promotes it, so no model sees it.
const completionRole = "completion"

// agentTurnCompleted reports whether a finished turn is an agent-mode turn,
// the one mode that ends without a model-written report. A goal or approved
// plan ends on the pass loop's report turn, and plan mode ends on the plan
// review, so neither gets the panel. The decision reads only harness state —
// the resolved mode and the loop sentinels — never reply text.
func agentTurnCompleted(res run.Result, planMode bool) bool {
	if planMode || res.GoalSentinel != "" || res.PlanSentinel != "" {
		return false
	}
	switch res.ModeDecision.Mode {
	case modes.ModePlan, modes.ModeGoal:
		return false
	}
	return true
}

// completionSummary renders the completion panel body from the transcript
// rows of the turn that just ended: the main-thread tool rows after the last
// non-steering user prompt. Subagent rows are someone else's work and are not
// counted. Every figure is a harness observation, never model text.
func completionSummary(msgs []components.Message, elapsed time.Duration) string {
	start := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" && !msgs[i].Steering {
			start = i + 1
			break
		}
	}
	tools, edits := 0, 0
	for _, m := range msgs[start:] {
		if m.Role != "tool" || m.SubagentID != "" {
			continue
		}
		tools++
		if components.IsEditTool(m.ToolName) {
			edits++
		}
	}
	parts := []string{"agent turn complete", countNoun(tools, "tool call"), countNoun(edits, "edit")}
	if elapsed > 0 {
		parts = append(parts, elapsed.Round(100*time.Millisecond).String())
	}
	return strings.Join(parts, " · ")
}

func countNoun(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// addCompletion appends the agent-mode completion panel.
func (a *App) addCompletion(elapsed time.Duration) {
	a.messages = append(a.messages, components.Message{
		Role:    completionRole,
		Content: completionSummary(a.messages, elapsed),
	})
}
