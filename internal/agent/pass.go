package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/clarify"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/readindex"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/todos"
	"github.com/vulnetix/belai/internal/tools"
	"github.com/vulnetix/belai/internal/transcript"
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
	// repairFailures counts consecutive iterations in which every tool result
	// was an error in the call itself (failures.go) and none succeeded. An
	// exhausted pass with a non-zero count was stopped by repairCap, so the
	// loop above answers it with a repair request, not a generic continuation.
	repairFailures int
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
	// askUser is the questionnaire the model sent through AskUserQuestion in
	// plan mode. It ends the pass; the plan loop hands it back so the
	// session can ask the user and continue in agent mode.
	askUser *clarify.Questionnaire
	// mutations counts the calls in this pass that the file-diff recorder saw
	// change at least one file. It is harness-observed, never model-claimed,
	// and it is the goal pass loop's primary progress signal.
	mutations int
	// mutatedPaths are the changed paths, deduplicated and bounded by
	// maxMutatedPaths. They are harness facts (paths only, never contents).
	mutatedPaths []string
	// selfVerified is true when, after the pass's last file mutation, a
	// non-inspection Bash command ran and exited 0 (tests, the script, a
	// build): a harness observation that the pass checked its own change.
	selfVerified bool
	// callSeq, lastMutSeq and lastCheckSeq order the pass's calls for
	// selfVerified.
	callSeq, lastMutSeq, lastCheckSeq int
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
const withheldRepairDirective = "Every tool result in the last two rounds was withheld. Read each reason above. An argument error (a bad path, a missing file) is fixed by re-issuing the call with corrected arguments — check the path form against the working directory and session roots in the system prompt. A classifier verdict is not an argument error: do not request that content again with any tool — proceed without it. If the task cannot be done without it, state that blocker in one line. Do not answer with a plan."

// toolRepairDirective is injected at a goal pass boundary when the pass that
// just ended executed no tool at all: every call it made was rejected before
// it ran, or it made none. The errors are already in the transcript, so this
// asks for the corrected call rather than restating them, and it names the
// edit as the deliverable so the repair pass does not turn into a report.
const toolRepairDirective = "That pass executed no tool successfully — every call was rejected before it ran. The rejection messages are above and each one names what was wrong with the arguments. Fix the arguments and re-issue the call now, starting with the edit that advances the goal. If a tool cannot be called at all, state which one and what it rejected, in one line."

// noteMutation folds one call's observed disk effect into the pass totals.
// readStreakNudge tracks tool rounds that changed no file and returns the
// directive to inject when the streak reaches readStreakNudgeAfter (and every
// readStreakNudgeAfter rounds after), else "". Only a turn that may edit is
// nudged: plan mode, a read-only agent turn, an explore subagent and the
// report pass are left alone. Goal mode gets the firm directive; agent mode
// the softer agentEditNudge, because an agent turn may be a question.
func (s *Session) readStreakNudge(streak, seen *int, mutations int, productive bool, mode modes.Mode) string {
	if mutations != *seen {
		*seen = mutations
		*streak = 0
		return ""
	}
	if !productive {
		return ""
	}
	*streak++
	every := readStreakNudgeAfter
	if s.turnRemediation {
		every = remediationNudgeAfter
	}
	if *streak%every != 0 {
		return ""
	}
	if s.planMode || s.turnReadOnly || s.exploreSubagent || s.reportOnly || s.kanbanWrapUpPass {
		return ""
	}
	if s.turnRemediation && mode != modes.ModePlan {
		return remediationNudge
	}
	switch mode {
	case modes.ModeGoal:
		if s.turnExecutePlan {
			// An approved plan may only read and report; telling it to
			// edit would push a finished read-only plan into inventing
			// a change.
			return planReadStreakDirective
		}
		if s.turnDecision {
			return decisionReadStreakDirective
		}
		return readStreakDirective
	case modes.ModeAgent:
		return agentEditNudge
	}
	return ""
}

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

// isChecklistTool reports whether name is a tool that reports the checklist:
// Todo in goal, agent and code mode, update_plan in plan mode.
func isChecklistTool(name string) bool {
	return strings.EqualFold(name, tools.TodoName) || strings.EqualFold(name, "update_plan")
}

// checklistFromArgs reconstructs the shared todo list from a Todo or
// update_plan call's arguments. It delegates to tools.ParseTodoArg, the single
// definition of the accepted shape (ParsePlanArg accepts the same), so the
// tool and the pass loop can never disagree about whether a call was usable.
func checklistFromArgs(args map[string]any) (todos.List, bool) {
	list, err := tools.ParseTodoArg(args)
	if err != nil {
		return todos.List{}, false
	}
	return list, true
}

// planPassIterations bounds one plan-mode pass. The plan is the deliverable,
// and every round is a full-context model call: at the general 40-round
// budget a planning pass read 114–138 files over 20+ minutes, the oldest
// results were cleared out of context as it grew, and it re-read them instead
// of writing (session b3a026a4, 99 minutes, no plan). Twelve rounds of
// parallel reads is enough to ground any plan; what is still open goes under
// Assumptions.
const planPassIterations = 12

// passBudget is the tool-round budget of one pass in mode: the session's
// iteration budget, capped at planPassIterations for a plan-mode pass.
func (s *Session) passBudget(mode modes.Mode) int {
	if mode == modes.ModePlan && s.maxIter > planPassIterations {
		return planPassIterations
	}
	return s.maxIter
}

// callUnit is one parsed, permission-checked tool call, used to decide the
// concurrent run without reordering.
type callUnit struct {
	call     rolemanager.ToolCall
	args     map[string]any
	parseErr error
	tool     tools.Tool
	decision permissions.Decision
	// readKey is set for a Read the read index can answer; repeat is the
	// harness note that answers it instead of running the read.
	readKey readindex.Key
	isRead  bool
	repeat  string
	// swap is set when the harness runs a builtin tool in place of this Bash
	// call; tool, args and decision then describe the builtin.
	swap *bashSwap
	// rewriteNote is the harness note that tells the model its Bash command
	// was changed by the user's bash_rewrite rules; args holds what ran.
	rewriteNote string
}

// execCall is the call that actually runs: the model's call, or the builtin
// that replaced it under the same id.
func (u callUnit) execCall() rolemanager.ToolCall {
	c := u.call
	c.Args = u.args
	if u.swap != nil {
		c.Name = u.swap.name
	}
	return c
}

// swappedFrom is the model's tool name when a swap happened, else "".
func (u callUnit) swappedFrom() string {
	if u.swap != nil {
		return u.call.Name
	}
	return ""
}

// ranAs is the tool name the UI shows for the call.
func (u callUnit) ranAs() string { return u.execCall().Name }

// turnContent is what the model reads: the result, with the swap note first
// when the harness ran another tool.
func (u callUnit) turnContent(result string) string {
	if u.swap == nil {
		return u.rewriteNote + result
	}
	return u.rewriteNote + u.swap.note() + result
}

// execCtx marks the context of a swapped call so its result is classified as
// Bash output would be.
func (u callUnit) execCtx(ctx context.Context) context.Context {
	if u.swap != nil {
		return withSwapped(ctx)
	}
	return ctx
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
	// repair counts consecutive iterations that failed only on errors in the
	// call itself and succeeded at nothing; mismatchFeedbacks counts responses
	// that named a tool that was not offered.
	var repair, mismatchFeedbacks int
	// readStreak counts tool rounds in a row that changed no file;
	// mutationsSeen is the pass's mutation count when it last reset.
	var readStreak, mutationsSeen int
	var text string
	var lastText string
	var updatePlan *todos.List
	// acc carries the pass's harness-observed disk effect across iterations;
	// finish folds it into whichever outcome the pass returns.
	var acc passOutcome
	// The kanban wrap-up continues from wherever the turn's last pass ended.
	defer func() { s.lastTurns = turns }()
	finish := func(o passOutcome) passOutcome {
		o.mutations = acc.mutations
		s.turnMutations += acc.mutations
		o.mutatedPaths = acc.mutatedPaths
		o.selfVerified = acc.mutations > 0 && acc.lastCheckSeq > acc.lastMutSeq
		if o.usage == nil {
			o.usage = acc.usage
		}
		o.spent = acc.spent
		return o
	}
	for i := 0; i < s.passBudget(mode); i++ {
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

		s.noteAssistantText(assistant.Text)
		if assistant.Text != "" {
			lastText = assistant.Text
			if text != "" {
				text += "\n"
			}
			text += assistant.Text
		}

		if len(assistant.ToolCalls) == 0 {
			if s.refuseRemediationFinish(acc.mutations, mode) {
				// The reply is kept; the model is asked once more for the edit.
				turns = append(turns, run.Turn{Role: "assistant", Content: assistant.Text})
				turns = append(turns, directiveTurns(remediationFinishGuard)...)
				continue
			}
			if open, total, ok := s.refuseOpenTodos(mode); ok {
				// A text-only reply with todos open is not a finish: the reply is
				// kept and the model is sent back for the open items.
				turns = append(turns, run.Turn{Role: "assistant", Content: assistant.Text})
				turns = append(turns, s.openTodoTurns(open, total)...)
				continue
			}
			return finish(passOutcome{reply: assistant.Text, usage: assistant.Usage, text: text, lastText: lastText, productive: productive}), turns, nil
		}

		mismatchPol := s.mismatchPolicy()
		filtered, err := rolemanager.CheckToolCalls(assistant.ToolCalls, s.callableNames(), mismatchPol)
		if err != nil {
			// The abort policy is fail-closed, but a call to a tool that was
			// not offered is the model's slip, and the closest offered name is
			// already known: answer every call with a result it can read and
			// let it re-issue. Only a model that keeps naming unavailable
			// tools ends the turn.
			mismatchFeedbacks++
			if mismatchFeedbacks > maxMismatchFeedbacks {
				return finish(passOutcome{text: text, lastText: lastText}), turns, err
			}
			turns = append(turns, run.Turn{Role: "assistant", Content: assistant.Text, ToolCalls: assistant.ToolCalls, Thinking: assistant.Thinking, ThinkingModel: run.ThinkingSource(s.cfg)})
			answers := mismatchResults(assistant.ToolCalls, s.callableNames())
			for _, call := range assistant.ToolCalls {
				result := answers[call.ID]
				callCopy := call
				callCopy.Args, _ = parseToolArgs(call)
				emit(Event{Kind: EventToolStartKind, Tool: &callCopy})
				emit(Event{Kind: EventToolResultKind, ToolName: call.Name, ToolCallID: call.ID, ToolResult: result})
				turns = append(turns, run.Turn{Role: "tool", Content: result, ToolCallID: call.ID, ToolName: call.Name})
			}
			continue
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
				result := truncatedArgsPrefix + "; re-issue the tool call with complete arguments"
				emit(Event{Kind: EventToolResultKind, ToolName: call.Name, ToolResult: result})
				turns = append(turns, run.Turn{
					Role:       "tool",
					Content:    result,
					ToolCallID: call.ID,
					ToolName:   call.Name,
				})
			}
			// A response cut off mid-call is an error in the call like any
			// other: bounded, so a model that cannot fit the call in its
			// output does not burn the whole budget.
			if repair++; repair >= s.repairCap() {
				return finish(passOutcome{exhausted: true, text: text, lastText: lastText, productive: productive, repairFailures: repair, updatePlan: updatePlan}), turns, nil
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
				if tool, ok := s.findCallable(call.Name); ok {
					u.tool = tool
					if call.Name == "Bash" {
						u.args, u.rewriteNote = s.rewriteBashArgs(args)
						args = u.args
					}
					u.decision, _, _ = s.decidePermission(call.Name, tool.Subject(args))
					if call.Name == "Bash" {
						// A builtin tool that fully replaces the command runs
						// instead, under the same call id (docs/jev-jobs.md).
						if sw := s.trySwap(ctx, pipe, args); sw != nil {
							u.swap, u.tool, u.args, u.decision = sw, sw.tool, sw.args, sw.decision
						}
					}
				}
			}
			// A Read the index already answers is not run again. Only an
			// allowed call on the advertised surface qualifies, so a deny
			// rule or the final plan pass's narrowing still applies.
			if u.tool != nil && u.decision == permissions.DecisionAllow {
				if _, refusal := s.execTool(call.Name); refusal == "" {
					if key, ok := s.readKey(call.Name, args); ok {
						u.readKey, u.isRead = key, true
						u.repeat = s.repeatedRead(key, turns)
					}
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
				callCopy := u.execCall()
				emit(Event{Kind: EventToolStartKind, Tool: &callCopy, SwappedFrom: u.swappedFrom()})
				wg.Add(1)
				go func(i int, u callUnit) {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()
					if u.repeat != "" {
						results[i] = u.repeat
						return
					}
					callCopy := u.execCall()
					start := time.Now()
					results[i] = s.executeCall(u.execCtx(ctx), callCopy, emit, &effects[i])
					took[i] = time.Since(start)
				}(i, u)
			}
			wg.Wait()
		}

		productiveIter := false
		allWithheld := len(units) > 0
		// failed collects this iteration's repairable failures for the repair
		// directive's note.
		var failed []string
		planExited := false
		planText := ""
		var askUser *clarify.Questionnaire
		for i := 0; i < len(units); i++ {
			u := units[i]
			if i >= concurrentEnd {
				callCopy := u.execCall()
				emit(Event{Kind: EventToolStartKind, Tool: &callCopy, SwappedFrom: u.swappedFrom()})
				if u.isRead {
					// Serial calls run after the ones before them in this
					// response, which may have changed the file: decide
					// again now rather than trusting the up-front answer.
					u.repeat = s.repeatedRead(u.readKey, turns)
				}
				switch {
				case u.parseErr != nil:
					results[i] = fmt.Sprintf("%s %q: %v.%s", malformedArgsPrefix, u.call.Name, u.parseErr, repairHint)
				case u.repeat != "":
					results[i] = u.repeat
				default:
					callCopy := u.execCall()
					start := time.Now()
					results[i] = s.executeCall(u.execCtx(ctx), callCopy, emit, &effects[i])
					took[i] = time.Since(start)
				}
			}
			acc.noteMutation(effects[i])
			acc.callSeq++
			if effects[i].changed {
				acc.lastMutSeq = acc.callSeq
			} else if isVerifyingBash(u.call.Name, u.args, results[i]) {
				acc.lastCheckSeq = acc.callSeq
			}
			// Every file a call changed drops out of the read index, so a
			// read after an edit always sees the new bytes. The stat check in
			// the index covers changes the recorder cannot see.
			s.reads.Invalidate(s.readRoot(), effects[i].paths...)
			if u.isRead && u.repeat == "" {
				s.recordRead(u.readKey, u.call.ID, results[i])
			}
			toolResult := results[i]
			if toolResult == tools.AskUserSentinel {
				var planQ *clarify.Questionnaire
				toolResult, planQ = s.handleAskUser(ctx, pipe, u.args, mode, emit)
				if planQ != nil {
					askUser = planQ
				}
				results[i] = toolResult
			}
			emit(Event{Kind: EventToolResultKind, ToolName: u.ranAs(), ToolCallID: u.call.ID, ToolResult: toolResult, Duration: took[i], SwappedFrom: u.swappedFrom()})
			turns = append(turns, run.Turn{
				Role:       "tool",
				Content:    u.turnContent(toolResult),
				ToolCallID: u.call.ID,
				ToolName:   u.call.Name,
				// Pixels a capture tool returned, already admitted by
				// imageguard; the request builder decides who can see them.
				Attachments: effects[i].images,
			})
			switch {
			case toolResult == tools.AskUserSentinel:
				productiveIter = true
				allWithheld = false
			case toolResult == tools.ExitPlanModeSentinel:
				planExited = true
				if p, ok := u.args["plan"].(string); ok {
					planText = p
				}
			case repairable(toolResult):
				// An error in the call itself: neither work nor a verdict. It
				// is not counted as an executed tool, so an iteration of
				// rejected calls stays an unproductive one.
				allWithheld = false
				failed = append(failed, failedNote(u.call.Name, toolResult))
			case !strings.HasPrefix(toolResult, withheldPrefix):
				// A call that ran counts as work, Todo and update_plan included. The
				// checklist is bookkeeping, but an iteration that adopted one
				// is not an empty iteration: counting it as empty used to fail
				// the whole goal loop with "pass N executed no tools" even
				// though the call succeeded.
				if isChecklistTool(u.call.Name) {
					if l, ok := checklistFromArgs(u.args); ok {
						updatePlan = &l
						s.adoptTodos(l)
					}
				} else if !s.kanbanWrapUpPass && !strings.HasPrefix(u.call.Name, "Kanban") {
					s.turnToolRuns++
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
		if nudge := s.readStreakNudge(&readStreak, &mutationsSeen, acc.mutations, productiveIter, mode); nudge != "" {
			turns = append(turns, directiveTurns(nudge)...)
		}
		if allWithheld {
			withheld++
		} else {
			withheld = 0
		}
		if len(failed) > 0 && !productiveIter {
			repair++
		} else {
			repair = 0
		}
		if askUser != nil && !planExited {
			return finish(passOutcome{reply: assistant.Text, usage: assistant.Usage, text: text, lastText: lastText, productive: productive, updatePlan: updatePlan, askUser: askUser}), turns, nil
		}
		if planExited {
			return finish(passOutcome{reply: assistant.Text, usage: assistant.Usage, text: text, lastText: lastText, productive: productive, planExit: true, planText: planText, updatePlan: updatePlan}), turns, nil
		}
		if repair >= s.repairCap() {
			return finish(passOutcome{exhausted: true, text: text, lastText: lastText, productive: productive, repairFailures: repair, updatePlan: updatePlan}), turns, nil
		}
		if repair == 2 {
			turns = append(turns, repairDirective(failed)...)
			continue
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

	return finish(passOutcome{exhausted: true, text: text, lastText: lastText, productive: productive, withheld: withheld, repairFailures: repair, updatePlan: updatePlan}), turns, nil
}
		if _, decided := s.recordedOutcome(); decided {
			// The worker recorded how its item ends: the turn is over (the goal
			// loop reads the record), so no further tool round is spent re-checking it.
			return finish(passOutcome{reply: assistant.Text, usage: assistant.Usage, text: text, lastText: lastText, productive: productive, updatePlan: updatePlan}), turns, nil
		}
