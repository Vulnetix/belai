package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/vulnetix/signet/internal/permissions"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/transcript"
)

// passOutcome is the result of one bounded tool-loop pass.
type passOutcome struct {
	reply     string
	usage     *transcript.Usage
	exhausted bool
	// text accumulates every non-empty assistant text of the pass. It is
	// model-authored assistant text and the only input the pass loop feeds to
	// todo-marker application — never tool results, never turns.
	text string
	// lastText is the most recent non-empty assistant text of the pass. On a
	// natural exit it equals reply; on an exhausted pass it is the model's
	// latest words and becomes the reply when GOAL_COMPLETE is accepted.
	lastText string
	// productive counts iterations that executed at least one non-withheld
	// tool result. The semantic-repair branch consumes an iteration without
	// doing any work, so a pass can exhaust itself entirely on truncation
	// repair; productive==0 must not buy another pass.
	productive int
}

// pass runs exactly one bounded tool-loop pass. It is the verbatim body of the
// pre-pass-loop iteration: drainSteer, streamTurnRetry, tool-call checking,
// the stop-reason "length" repair, and the per-call execute loop. It returns
// the mutated turns — tool results accumulate in it across passes.
func (s *Session) pass(ctx context.Context, pipe *rolemanager.Pipeline, system string, turns []run.Turn, streaming bool, emit func(Event)) (passOutcome, []run.Turn, error) {
	var productive int
	var text string
	var lastText string
	for i := 0; i < s.maxIter; i++ {
		turns = append(turns, s.drainSteer(ctx, pipe, emit)...)
		assistant, err := s.streamTurnRetry(ctx, system, turns, streaming, emit)
		if err != nil {
			return passOutcome{text: text, lastText: lastText}, turns, err
		}

		if assistant.Text != "" {
			lastText = assistant.Text
			if text != "" {
				text += "\n"
			}
			text += assistant.Text
		}

		if len(assistant.ToolCalls) == 0 {
			return passOutcome{reply: assistant.Text, usage: assistant.Usage, text: text, lastText: lastText, productive: productive}, turns, nil
		}

		mismatchPol := s.mismatchPolicy()
		filtered, err := rolemanager.CheckToolCalls(assistant.ToolCalls, s.registry.Names(), mismatchPol)
		if err != nil {
			return passOutcome{text: text, lastText: lastText}, turns, err
		}

		// Append assistant turn containing its tool_calls. Use the filtered
		// set so a PolicyStrip turn matches the tool turns that follow.
		turns = append(turns, run.Turn{
			Role:      "assistant",
			Content:   assistant.Text,
			ToolCalls: filtered,
		})

		// Semantic repair: if the model ran out of tokens mid-tool-call, do
		// not execute partially-specified arguments. Refuse the whole set as
		// isError results so the model can re-issue in the next iteration.
		if assistant.StopReason == "length" && len(filtered) > 0 {
			for _, call := range filtered {
				emit(Event{Kind: EventToolStartKind, Tool: &call})
				result := "tool result withheld: arguments may be truncated; re-issue the tool call with complete arguments"
				emit(Event{Kind: EventToolResultKind, ToolName: call.Name, ToolResult: result})
				turns = append(turns, run.Turn{
					Role:       "tool",
					Content:    result,
					ToolCallID: call.ID,
					ToolName:   call.Name,
				})
			}
			continue
		}

		productiveIter := false
		for _, call := range filtered {
			if s.permissionDecision(call) == permissions.DecisionAsk {
				emit(Event{Kind: EventPermissionAskKind, AskName: call.Name})
			}
			emit(Event{Kind: EventToolStartKind, Tool: &call})

			args, parseErr := parseToolArgs(call)
			if parseErr != nil {
				toolResult := fmt.Sprintf("tool result withheld: malformed arguments for %q: %v", call.Name, parseErr)
				emit(Event{Kind: EventToolResultKind, ToolName: call.Name, ToolResult: toolResult})
				turns = append(turns, run.Turn{
					Role:       "tool",
					Content:    toolResult,
					ToolCallID: call.ID,
					ToolName:   call.Name,
				})
				continue
			}
			callCopy := call
			callCopy.Args = args
			toolResult := s.executeCall(ctx, callCopy, emit)
			emit(Event{Kind: EventToolResultKind, ToolName: call.Name, ToolResult: toolResult})
			turns = append(turns, run.Turn{
				Role:       "tool",
				Content:    toolResult,
				ToolCallID: call.ID,
				ToolName:   call.Name,
			})
			if !strings.HasPrefix(toolResult, "tool result withheld:") {
				productiveIter = true
			}
		}
		if productiveIter {
			productive++
		}
	}

	return passOutcome{exhausted: true, text: text, lastText: lastText, productive: productive}, turns, nil
}
