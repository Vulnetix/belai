package agent

import (
	"context"
	"fmt"

	"github.com/vulnetix/belai/internal/explore"
	"github.com/vulnetix/belai/internal/tools"
)

// maxTaskCallsPerTurn caps the number of Task invocations in one fan-out
// turn. Several Task calls in one assistant response already run concurrently,
// so a modest cap keeps the turn bounded without serialising independent work.
const maxTaskCallsPerTurn = 12

// taskCallsThisTurn counts Task calls in the current turn. It is reset by the
// agent run before the tool loop begins.
type taskCounter int

// runTask executes one read-only subagent for the Task tool. It runs on the
// plan surface (no Task recursion) and returns a KindSubagent result that
// classifies before promotion.
func (s *Session) runTask(ctx context.Context, description, prompt string) (tools.Result, error) {
	if s.taskCallsThisTurn >= maxTaskCallsPerTurn {
		return tools.Result{}, fmt.Errorf("Task budget exhausted for this turn")
	}
	s.taskCallsThisTurn++

	g := s.groundingProbe(ctx)
	t := explore.Task{
		Index:     s.taskCallsThisTurn - 1,
		Reference: description,
		Prompt:    prompt + explore.ReportContract(),
		Kind:      explore.RefText,
		Budget:    s.settings.Resilience.MaxExploreIterationsOr(4),
	}

	id := fmt.Sprintf("task-%d", t.Index)
	body := s.runSubagent(ctx, t, g, nil, id, func(e Event) {
		// Subagent lifecycle events for Task reuse the existing explore
		// event kinds; the TUI renders them in the runs panel.
		if s.emit != nil {
			s.emit(e)
		}
	}, false)
	return tools.Result{Kind: tools.KindSubagent, Content: body}, nil
}
