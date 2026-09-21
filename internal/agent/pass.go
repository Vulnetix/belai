package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/vulnetix/signet/internal/permissions"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/todos"
	"github.com/vulnetix/signet/internal/tools"
	"github.com/vulnetix/signet/internal/transcript"
)

// toolConcurrency caps how many read-only tool calls execute in parallel.
// Unbounded fan-out against a rate-limited provider produces 429s, which is
// worse than sequential.
const toolConcurrency = 4

// concurrentEnd returns the length of the leading run of read-only,
// permission-allowed, parseable calls. A mutating tool ends the run, so a Read
// after a Write observes the write.
func concurrentEnd(units []callUnit) int {
	end := 0
	for end < len(units) {
		u := units[end]
		if u.parseErr != nil || u.tool == nil || !u.tool.Kind().ReadOnly() || u.decision != permissions.DecisionAllow {
			break
		}
		end++
	}
	return end
}

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
	// withheld counts consecutive iterations where every tool result was
	// withheld (plan mode denies writes). Two in a row injects a directive
	// explaining that the plan must be produced as reply text; three breaks
	// the pass to stop the spin.
	withheld int
	// planExit is set when the model called ExitPlanMode. The caller treats
	// planExit is true when the pass called ExitPlanMode with a valid plan.
	// The pass loop treats this as a clean completion signal rather than a
	// normal no-tool exit.
	planExit bool
	// planText is the plan argument the model handed to ExitPlanMode. It is
	// threaded to the plan file so the recorded artifact is the deliberately
	// authored plan, not the latest assistant reply.
	planText string
	// updatePlan is a checklist the model reported through the update_plan
	// tool. When non-nil the pass loop adopts it into the shared todo list.
	updatePlan *todos.List
	// mutations counts the calls in this pass that the file-diff recorder saw
	// change at least one file. It is harness-observed, never model-claimed,
	// and it is the goal pass loop's primary progress signal.
	mutations int
	// mutatedPaths are the changed paths, deduplicated and bounded by
	// maxMutatedPaths. They are harness facts (paths only, never contents).
	mutatedPaths []string
}

// maxMutatedPaths bounds the changed-path list carried out of a pass. The list
// becomes harness-fact evidence for the goal evaluator, so it is capped rather
// than allowed to grow with a large refactor.
const maxMutatedPaths = 20

// noteMutation folds one call's observed disk effect into the pass totals.
func (o *passOutcome) noteMutation(eff callEffect) {
	if !eff.changed {
		return
	}
	o.mutations++
	for _, p := range eff.paths {
		if len(o.mutatedPaths) >= maxMutatedPaths {
			return
		}
		if !slices.Contains(o.mutatedPaths, p) {
			o.mutatedPaths = append(o.mutatedPaths, p)
		}
	}
}

// updatePlanFromArgs reconstructs the shared todo list from an update_plan
// call's arguments, mirroring tools.UpdatePlan.Execute so the pass loop can
// adopt the model's reported checklist into the ledger without a second write
// path.
func updatePlanFromArgs(args map[string]any) (todos.List, bool) {
	raw, ok := args["plan"].([]any)
	if !ok || len(raw) == 0 {
		return todos.List{}, false
	}
	var items []todos.Item
	for i, r := range raw {
		m, ok := r.(map[string]any)
		if !ok {
			return todos.List{}, false
		}
		step, _ := m["step"].(string)
		status, _ := m["status"].(string)
		if step == "" {
			return todos.List{}, false
		}
		st := todos.StatusPending
		switch status {
		case "in_progress":
			st = todos.StatusActive
		case "completed":
			st = todos.StatusDone
		}
		items = append(items, todos.Item{N: i + 1, Text: step, Status: st})
	}
	return todos.List{Items: items}, true
}

// callUnit is one parsed, permission-checked tool call, used to decide the
// concurrent run without reordering.
type callUnit struct {
	call     rolemanager.ToolCall
	args     map[string]any
	parseErr error
	tool     tools.Tool
	decision permissions.Decision
}

// pass runs exactly one bounded tool-loop pass. It is the verbatim body of the
// pre-pass-loop iteration: drainSteer, streamTurnRetry, tool-call checking,
// the stop-reason "length" repair, and the per-call execute loop. It returns
// the mutated turns — tool results accumulate in it across passes.
func (s *Session) pass(ctx context.Context, pipe *rolemanager.Pipeline, system string, turns []run.Turn, streaming bool, emit func(Event)) (passOutcome, []run.Turn, error) {
	var productive int
	var withheld int
	var text string
	var lastText string
	var updatePlan *todos.List
	// acc carries the pass's harness-observed disk effect across iterations;
	// finish folds it into whichever outcome the pass returns.
	var acc passOutcome
	finish := func(o passOutcome) passOutcome {
		o.mutations = acc.mutations
		o.mutatedPaths = acc.mutatedPaths
		return o
	}
	for i := 0; i < s.maxIter; i++ {
		updatePlan = nil
		turns = append(turns, s.drainSteer(ctx, pipe, emit)...)
		assistant, err := s.streamTurnRetry(ctx, system, turns, streaming, emit)
		if err != nil {
			return finish(passOutcome{text: text, lastText: lastText}), turns, err
		}

		if assistant.Text != "" {
			lastText = assistant.Text
			if text != "" {
				text += "\n"
			}
			text += assistant.Text
		}

		if len(assistant.ToolCalls) == 0 {
			return finish(passOutcome{reply: assistant.Text, usage: assistant.Usage, text: text, lastText: lastText, productive: productive}), turns, nil
		}

		mismatchPol := s.mismatchPolicy()
		filtered, err := rolemanager.CheckToolCalls(assistant.ToolCalls, s.registry.Names(), mismatchPol)
		if err != nil {
			return finish(passOutcome{text: text, lastText: lastText}), turns, err
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
		units := make([]callUnit, len(filtered))
		for i, call := range filtered {
			args, parseErr := parseToolArgs(call)
			u := callUnit{call: call, args: args, parseErr: parseErr}
			if parseErr == nil {
				if tool, ok := s.registry.Find(call.Name); ok {
					u.tool = tool
					u.decision, _, _ = s.decidePermission(call.Name, tool.Subject(args))
				}
			}
			units[i] = u
		}

		// The concurrent group is the leading run of read-only, permission-
		// allowed, parseable calls. A mutating kind (Write, Edit, full Bash)
		// ends the run: a Read after a Write that wrote the file must observe
		// the write, so nothing reorders across a mutating call. A permission
		// ask ends the run because two concurrent asks would race the UI.
		concurrentEnd := concurrentEnd(units)

		results := make([]string, len(units))
		// One effect slot per call: the concurrent fan-out is read-only by
		// construction, but each goroutine still writes its own slot so the
		// observation needs no locking.
		effects := make([]callEffect, len(units))
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
					results[i] = s.executeCall(ctx, callCopy, emit, &effects[i])
				}(i, u)
			}
			wg.Wait()
		}

		productiveIter := false
		allWithheld := len(units) > 0
		planExited := false
		planText := ""
		for i := 0; i < len(units); i++ {
			u := units[i]
			if i >= concurrentEnd {
				emit(Event{Kind: EventToolStartKind, Tool: &u.call})
				if u.parseErr != nil {
					results[i] = fmt.Sprintf("tool result withheld: malformed arguments for %q: %v", u.call.Name, u.parseErr)
				} else {
					callCopy := u.call
					callCopy.Args = u.args
					results[i] = s.executeCall(ctx, callCopy, emit, &effects[i])
				}
			}
			acc.noteMutation(effects[i])
			toolResult := results[i]
			emit(Event{Kind: EventToolResultKind, ToolName: u.call.Name, ToolCallID: u.call.ID, ToolResult: toolResult})
			turns = append(turns, run.Turn{
				Role:       "tool",
				Content:    toolResult,
				ToolCallID: u.call.ID,
				ToolName:   u.call.Name,
			})
			switch {
			case toolResult == tools.ExitPlanModeSentinel:
				planExited = true
				if p, ok := u.args["plan"].(string); ok {
					planText = p
				}
			case u.call.Name == "update_plan":
				if l, ok := updatePlanFromArgs(u.args); ok {
					updatePlan = &l
				}
			case !strings.HasPrefix(toolResult, "tool result withheld:"):
				productiveIter = true
				allWithheld = false
			default:
				allWithheld = allWithheld && true
			}
		}
		if productiveIter {
			productive++
		}
		if allWithheld {
			withheld++
		} else {
			withheld = 0
		}
		if planExited {
			return finish(passOutcome{reply: assistant.Text, usage: assistant.Usage, text: text, lastText: lastText, productive: productive, planExit: true, planText: planText, updatePlan: updatePlan}), turns, nil
		}
		if withheld == 2 {
			turns = append(turns, directiveTurns("Writes are unavailable in plan mode. Put the plan in your reply text, then call ExitPlanMode to finish.")...)
			continue
		}
		if withheld >= 3 {
			return finish(passOutcome{exhausted: true, text: text, lastText: lastText, productive: productive, withheld: withheld, updatePlan: updatePlan}), turns, nil
		}
	}

	return finish(passOutcome{exhausted: true, text: text, lastText: lastText, productive: productive, withheld: withheld, updatePlan: updatePlan}), turns, nil
}
