package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/vulnetix/signet/internal/permissions"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/tools"
	"github.com/vulnetix/signet/internal/transcript"
)

// toolConcurrency caps how many read-only tool calls execute in parallel.
// Unbounded fan-out against a rate-limited provider produces 429s, which is
// worse than sequential.
const toolConcurrency = 4

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

		// Parse and permission-check every call up front so the concurrent run
		// can be decided without reordering.
		type callUnit struct {
			call     rolemanager.ToolCall
			args     map[string]any
			parseErr error
			tool     tools.Tool
			decision permissions.Decision
		}
		units := make([]callUnit, len(filtered))
		for i, call := range filtered {
			args, parseErr := parseToolArgs(call)
			u := callUnit{call: call, args: args, parseErr: parseErr}
			if parseErr == nil {
				if tool, ok := s.registry.Find(call.Name); ok {
					u.tool = tool
					u.decision, _ = s.decidePermission(call.Name, tool.Subject(args))
				}
			}
			units[i] = u
		}

		// The concurrent group is the leading run of read-only, permission-
		// allowed, parseable calls. The sole mutating kind is Bash, and a Read
		// after a Bash that wrote the file must observe the write, so nothing
		// reorders across a Bash call. A permission ask ends the run because
		// two concurrent asks would race the UI.
		concurrentEnd := 0
		for concurrentEnd < len(units) {
			u := units[concurrentEnd]
			if u.parseErr != nil || u.tool == nil || !u.tool.Kind().ReadOnly() || u.decision != permissions.DecisionAllow {
				break
			}
			concurrentEnd++
		}

		results := make([]string, len(units))
		if concurrentEnd > 0 {
			sem := make(chan struct{}, toolConcurrency)
			var wg sync.WaitGroup
			for i := 0; i < concurrentEnd; i++ {
				u := units[i]
				emit(Event{Kind: EventToolStartKind, Tool: &u.call})
				wg.Add(1)
				go func(i int, u callUnit) {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()
					callCopy := u.call
					callCopy.Args = u.args
					results[i] = s.executeCall(ctx, callCopy, emit)
				}(i, u)
			}
			wg.Wait()
		}

		productiveIter := false
		for i := 0; i < len(units); i++ {
			u := units[i]
			if i >= concurrentEnd {
				if u.decision == permissions.DecisionAsk {
					emit(Event{Kind: EventPermissionAskKind, AskName: u.call.Name})
				}
				emit(Event{Kind: EventToolStartKind, Tool: &u.call})
				if u.parseErr != nil {
					results[i] = fmt.Sprintf("tool result withheld: malformed arguments for %q: %v", u.call.Name, u.parseErr)
				} else {
					callCopy := u.call
					callCopy.Args = u.args
					results[i] = s.executeCall(ctx, callCopy, emit)
				}
			}
			toolResult := results[i]
			emit(Event{Kind: EventToolResultKind, ToolName: u.call.Name, ToolCallID: u.call.ID, ToolResult: toolResult})
			turns = append(turns, run.Turn{
				Role:       "tool",
				Content:    toolResult,
				ToolCallID: u.call.ID,
				ToolName:   u.call.Name,
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
