package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/modes"
	"github.com/vulnetix/signet/internal/permissions"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/todos"
	"github.com/vulnetix/signet/internal/tools"
	"github.com/vulnetix/signet/internal/transcript"
)

// minToolConcurrency and maxToolConcurrency bound how many read-only tool
// calls of one assistant turn execute in parallel. Local tools do not touch
// the provider, so the bound follows the user's max_agents fan-out setting
// rather than a fixed 4, clamped so a huge setting cannot fork-bomb the host.
const (
	minToolConcurrency = 4
	maxToolConcurrency = 16
)

// toolConcurrency returns the parallel read-only tool-call bound for this
// session: resilience.max_agents clamped to [minToolConcurrency,
// maxToolConcurrency].
func (s *Session) toolConcurrency() int {
	n := s.settings.Resilience.MaxAgentsOr(config.DefaultMaxAgents)
	if n < minToolConcurrency {
		return minToolConcurrency
	}
	if n > maxToolConcurrency {
		return maxToolConcurrency
	}
	return n
}

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
	reply string
	// usage is the most recent provider call's usage (its prompt tokens are the
	// live context size). It is set on every exit path, exhausted included.
	usage *transcript.Usage
	// spent is the total tokens of every provider call the pass made. A pass
	// usually makes several calls, so this — not usage — is what goal
	// accounting adds up.
	spent     int
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

// withheldRepairDirective is injected when an agent/goal-mode pass ends with
// two consecutive all-withheld iterations. Unlike plan mode — where withheld
// writes mean the mode is read-only — these modes advertise the full mutating
// surface, so a withheld streak means the model's calls are failing and must be
// re-issued with corrected arguments. It never names ExitPlanMode, which is not
// advertised outside plan mode.
const withheldRepairDirective = "Every tool result in the last two rounds was withheld. Read each reason above. An argument error (a bad path, a missing file) is fixed by re-issuing the call with corrected arguments — check the path form against the working directory and session roots in the system prompt. A classifier verdict is not an argument error: do not request that content again; use Grep for the specific lines or proceed without it. If neither works, state the blocker in one line. Do not answer with a plan."

// toolRepairDirective is injected at a goal pass boundary when the pass that
// just ended executed no tool at all: every call it made was rejected before
// it ran, or it made none. The errors are already in the transcript, so this
// asks for the corrected call rather than restating them, and it names the
// edit as the deliverable so the repair pass does not turn into a report.
const toolRepairDirective = "That pass executed no tool successfully — every call was rejected before it ran. The rejection messages are above and each one names what was wrong with the arguments. Fix the arguments and re-issue the call now, starting with the edit that advances the goal. If a tool cannot be called at all, state which one and what it rejected, in one line."

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
// call's arguments. It delegates to tools.ParsePlanArg, the single definition
// of the accepted shape, so the tool and the pass loop can never disagree
// about whether a call was usable.
func updatePlanFromArgs(args map[string]any) (todos.List, bool) {
	list, err := tools.ParsePlanArg(args)
	if err != nil {
		return todos.List{}, false
	}
	return list, true
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
//
// mode is the engaged interaction mode. It is threaded explicitly so the
// withheld-repair directive can tell the model the truth: plan mode has no
// write tools and its deliverable is prose, while agent/goal mode has the
// full mutating surface and a withheld streak means the tools are broken.
func (s *Session) pass(ctx context.Context, pipe *rolemanager.Pipeline, system string, turns []run.Turn, streaming bool, emit func(Event), mode modes.Mode) (passOutcome, []run.Turn, error) {
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
		if o.usage == nil {
			o.usage = acc.usage
		}
		o.spent = acc.spent
		return o
	}
	for i := 0; i < s.maxIter; i++ {
		updatePlan = nil
		turns = append(turns, s.drainSteer(ctx, pipe, emit)...)
		s.clearStaleToolResults(turns)
		assistant, err := s.streamTurnRetry(ctx, system, turns, streaming, emit)
		if err != nil {
			return finish(passOutcome{text: text, lastText: lastText}), turns, err
		}
		if assistant.Usage != nil {
			acc.usage = assistant.Usage
			acc.spent += assistant.Usage.Total()
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
		// Signed thinking rides on the turn so the next request of the tool
		// loop can echo it back, as the provider requires.
		turns = append(turns, run.Turn{
			Role:          "assistant",
			Content:       assistant.Text,
			ToolCalls:     filtered,
			Thinking:      assistant.Thinking,
			ThinkingModel: run.ThinkingSource(s.cfg),
		})

		// Semantic repair: if the model ran out of tokens mid-tool-call, do
		// not execute partially-specified arguments. Refuse the whole set as
		// isError results so the model can re-issue in the next iteration.
		if assistant.StopReason == "length" && len(filtered) > 0 {
			for _, call := range filtered {
				callCopy := call
				callCopy.Args, _ = parseToolArgs(call)
				emit(Event{Kind: EventToolStartKind, Tool: &callCopy})
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
		// took is each call's execution time, classification included: what the
		// call cost the turn.
		took := make([]time.Duration, len(units))
		if concurrentEnd > 0 {
			sem := make(chan struct{}, s.toolConcurrency())
			var wg sync.WaitGroup
			for i := 0; i < concurrentEnd; i++ {
				u := units[i]
				callCopy := u.call
				callCopy.Args = u.args
				emit(Event{Kind: EventToolStartKind, Tool: &callCopy})
				wg.Add(1)
				go func(i int, u callUnit) {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()
					callCopy := u.call
					callCopy.Args = u.args
					start := time.Now()
					results[i] = s.executeCall(ctx, callCopy, emit, &effects[i])
					took[i] = time.Since(start)
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
				callCopy := u.call
				callCopy.Args = u.args
				emit(Event{Kind: EventToolStartKind, Tool: &callCopy})
				if u.parseErr != nil {
					results[i] = fmt.Sprintf("tool result withheld: malformed arguments for %q: %v", u.call.Name, u.parseErr)
				} else {
					callCopy := u.call
					callCopy.Args = u.args
					start := time.Now()
					results[i] = s.executeCall(ctx, callCopy, emit, &effects[i])
					took[i] = time.Since(start)
				}
			}
			acc.noteMutation(effects[i])
			toolResult := results[i]
			emit(Event{Kind: EventToolResultKind, ToolName: u.call.Name, ToolCallID: u.call.ID, ToolResult: toolResult, Duration: took[i]})
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
			case !strings.HasPrefix(toolResult, "tool result withheld:"):
				// A call that ran counts as work, update_plan included. The
				// checklist is bookkeeping, but an iteration that adopted one
				// is not an empty iteration: counting it as empty used to fail
				// the whole goal loop with "pass N executed no tools" even
				// though the call succeeded.
				if u.call.Name == "update_plan" {
					if l, ok := updatePlanFromArgs(u.args); ok {
						updatePlan = &l
					}
				}
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
			if mode == modes.ModePlan {
				turns = append(turns, directiveTurns("Writes are unavailable in plan mode. Put the plan in your reply text, then call ExitPlanMode to finish.")...)
			} else {
				turns = append(turns, directiveTurns(withheldRepairDirective)...)
			}
			continue
		}
		if withheld >= 3 {
			return finish(passOutcome{exhausted: true, text: text, lastText: lastText, productive: productive, withheld: withheld, updatePlan: updatePlan}), turns, nil
		}
	}

	return finish(passOutcome{exhausted: true, text: text, lastText: lastText, productive: productive, withheld: withheld, updatePlan: updatePlan}), turns, nil
}
