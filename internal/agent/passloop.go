package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/vulnetix/signet/internal/goals"
	"github.com/vulnetix/signet/internal/modelinfo"
	"github.com/vulnetix/signet/internal/modes"
	"github.com/vulnetix/signet/internal/plans"
	"github.com/vulnetix/signet/internal/repomap"
	"github.com/vulnetix/signet/internal/resilience"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/sanitize"
	"github.com/vulnetix/signet/internal/todos"
	"github.com/vulnetix/signet/internal/transcript"
)

// ErrPassLoopCancelled reports a deliberate cancellation of the goal pass
// loop. It is distinct from a raw context.Canceled so the UI can show a clean
// "cancelled" notice instead of an agent error.
var ErrPassLoopCancelled = errors.New("goal pass loop cancelled")

// Goal-mode pass-loop tuning. These are stall detectors, not ceilings: they
// can never stop a loop that is making measurable progress. See
// docs/role-manager.md, "Goal pass loop", for the normative description.
const (
	// goalVerifyEvery: every Nth consecutive GOAL_PARTIAL verdict without a
	// todo-state transition becomes a verification pass — one whose injected
	// instruction is to check the plan against the files on disk.
	// GOAL_COMPLETE is only accepted after at least one such pass ran.
	goalVerifyEvery = 2
	// goalStallPartial: consecutive GOAL_PARTIAL verdicts without a
	// todo-state transition before the loop provides session context and
	// resets progression tracking. The model is busy but not advancing;
	// two full verification cycles buy nothing, so the harness injects a
	// stronger progression directive and starts a new agentic evaluation
	// loop rather than aborting.
	goalStallPartial = 2 * goalVerifyEvery
	// goalNoWritePasses: consecutive passes that changed no file before the
	// loop injects the no-write directive. Two passes is enough reading to
	// locate any edit worth making; a third spent read-only is the failure
	// mode goal mode exists to avoid.
	goalNoWritePasses = 2
	// maxMalformedEvals: consecutive malformed evaluator replies (each one
	// already re-asked once with corrective feedback) before the loop stops
	// consulting the evaluator and returns the work so far.
	maxMalformedEvals = 2
	// maxGoalEvalErrors: consecutive goal-evaluator transport failures before
	// the loop stops consulting the evaluator and returns the work so far. A
	// single provider error at a pass boundary must not throw away a long run,
	// but a permanently unreachable evaluator must not grant unbounded passes.
	maxGoalEvalErrors = 2
	// maxUnproductivePasses: consecutive passes that executed no tool at all
	// before the loop stops. The first one is repaired rather than fatal: a
	// rejected argument shape costs a pass without meaning the run is over,
	// and failing the goal there discards every pass that did work.
	maxUnproductivePasses = 2
	// compactThresholdPct: compact at the pass boundary when the estimated
	// context exceeds this share of the model window.
	compactThresholdPct = 70
	// defaultCompactWindow is the window compaction assumes when nothing
	// knows the model's real one. It is deliberately conservative: compacting
	// early costs one classifier call, never compacting costs the run.
	defaultCompactWindow = 128_000
	// defaultAgentContinuations bounds budget-exhaustion wrap-up passes in
	// agent and plan mode when resilience.max_passes is unset (0). Goal mode
	// treats 0 as unbounded by design; agent/plan mode must not inherit that.
	defaultAgentContinuations = 5
)

// Harness-injected continuation instructions for the pass boundary. The body
// is sealed into a <directive> block at egress (Turn.Directive); the same body
// is wrapped in DirectivePrefix/DirectiveSuffix prose so the model reads the
// sealed block as context rather than as the question to answer.
const (
	planDirective         = "No work has landed yet. Name the file to change and make the smallest correct edit that advances the goal, in this pass. Record the steps with update_plan (first step in_progress) if you have not already; the list is a side effect of working, not a substitute for it. Read the exact bytes first, then edit immediately — do not end this pass without a file mutation."
	verificationDirective = "Before doing any further work, verify the completed items in the todo list against the files on disk (read-only). Confirm each marked-done item is actually true; if one is not, correct the list and the work. Only continue new work after the check."
	// continuationDirective is injected when a bounded pass spends its whole
	// iteration budget. Budget exhaustion is a turn boundary, not a failure.
	continuationDirective = "The tool budget for this turn was reached. If the work needs a file change, make the edit now rather than reading further; if more tool calls are needed to finish, make them now; otherwise give the final answer. Either way, say briefly what was done and what remains."
	// agentEditNudge leads the continuation directive when an agent-mode turn
	// has spent a full tool budget without changing a file. Sessions showed
	// agent mode reading until the context window filled; this is the agent
	// twin of goal mode's noWriteDirective, softened because an agent turn
	// may legitimately be a question.
	agentEditNudge = "You have read enough to act. If the request needs a change, make the edit now; if something blocks it, state the blocker in one line. Do not re-read files you have already read in full."
	// planExecuteDirective leads the first pass of an approved plan. The user
	// already approved the plan, so there is nothing left to confirm or
	// re-explore: the first unchecked step is the first edit.
	planExecuteDirective = "The approved plan in the system prompt is your objective and it is already approved — do not re-plan, re-explore, or ask for confirmation. Execute it in order: the first unfinished step is the first edit of this pass."
	// goalAckDirective is injected on the first goal pass. Goal mode's point
	// over plan mode is that the work starts now rather than after a review,
	// so the checklist rides in the same response as the first actions — but
	// the first actions are whatever the work needs, reading included; the
	// no-write escalations at later boundaries catch a goal that never edits.
	goalAckDirective = "Start the work in this pass. In the same response as your first actions, call update_plan once with the steps you will execute, the first marked in_progress. Batch the reads you need in parallel, then make the change from the exact bytes you read. Mark steps complete with update_plan, or with [DONE:n] in your reply, as you finish them. Keep any restatement of the objective to a single line naming the deliverable and how completion will be verified."
)

// goalAckDirective returns the first-pass goal directive, naming the detected
// test commands as the default verification surface when the repo map knows
// them. Test commands from added workspace directories are unioned in so the
// verification surface covers every root.
func (s *Session) goalAckDirective() string {
	directive := goalAckDirective
	if s.turnExecutePlan {
		directive = planExecuteDirective + " " + directive
	}
	cmds := s.allTestCommands()
	if len(cmds) == 0 {
		return directive
	}
	return directive + " The default verification surface is: " + strings.Join(cmds, "; ") + "."
}

// allTestCommands returns the union of Commands.Test across the primary repo
// map and any workspace directory maps.
func (s *Session) allTestCommands() []string {
	seen := map[string]bool{}
	var out []string
	add := func(m *repomap.Map) {
		if m == nil {
			return
		}
		for _, c := range m.Commands.Test {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	add(s.repoMap)
	for i := range s.workspaceMaps {
		add(&s.workspaceMaps[i])
	}
	return out
}

// passLedger is the loop-local decision state of one goal pass loop. Pass
// index, verification-pass count and sentinel history live here — never in
// turns and never re-parsed out of transcript text. If any of this were
// derived from content, a SAFE-classified file containing the right token
// could flip harness state.
type passLedger struct {
	passes   int
	goalText string

	// todo list shared by goal mode, plan pursual and the TUI panel.
	list          todos.List
	hasList       bool
	todoChanged   bool // true when the pass that just ended advanced the list
	lastTodoState string

	// surveyPending: a forced codebase survey was injected before the pass
	// about to run; carried into that pass's EventPassKind.
	surveyPending bool
	// surveyedOnce: the forced codebase survey has already run for this goal,
	// so a repeat GOAL_NOT_STARTED gets the action directive alone. A second
	// survey buys more reading, which is never what a not-started goal is
	// short of.
	surveyedOnce bool

	// verificationArmed: the pass about to run carries the verification
	// directive; verificationPasses counts finished verification passes.
	verificationArmed  bool
	verificationPasses int

	// partialStreak counts consecutive no-progress PARTIAL verdicts; a todo
	// transition resets it. malformedStreak counts consecutive malformed
	// evaluator replies; a clean reply resets it. evalErrorStreak counts
	// consecutive evaluator transport failures; any evaluator contact
	// (clean or malformed) resets it.
	partialStreak   int
	malformedStreak int
	evalErrorStreak int

	// writes is the number of file-changing tool calls observed across the
	// whole goal; passWrites is the count for the pass that just ended and
	// passesSinceWrite the run of passes that changed nothing. These are
	// harness observations from the file-diff recorder, never model claims,
	// and they are goal mode's primary progress signal: the loop exists to
	// produce changes on disk, not a finished survey.
	writes           int
	passWrites       int
	passesSinceWrite int
	// passWithheld is the withheld count for the pass that just ended and
	// withheldPasses how many passes ended with every tool result withheld.
	// Together they let the loop tell a broken tool resolver from a model
	// that is simply not writing.
	passWithheld   int
	withheldPasses int
	// touched are the paths changed so far, deduplicated and bounded. Paths
	// and counts only — never file contents.
	touched []string

	// overflowRetried: a ClassOverflow escaping pass is caught once (compact,
	// re-run the pass); a second overflow is terminal.
	overflowRetried bool

	// unproductivePasses counts consecutive passes that executed no tool at
	// all. One such pass is repairable — a rejected argument shape or a
	// truncation repair costs a pass without meaning the run is over — so the
	// loop injects a corrective directive and tries again. Two in a row is a
	// model that cannot call a tool, and the loop stops.
	unproductivePasses int
}

// turnState renders the todo list as a state key for progress detection.
func (l *passLedger) turnState() string {
	if !l.hasList {
		return ""
	}
	var b strings.Builder
	for _, it := range l.list.Items {
		fmt.Fprintf(&b, "%d:%s ", it.N, it.Status)
	}
	return b.String()
}

// advanceTodos maintains the shared list from model-authored assistant text
// only. out.text is the pass's accumulated assistant text — never tool
// results, never turns. A "[DONE:n]" marker in a repository file must not
// mark work complete: completion is an input to whether the loop stops.
func (l *passLedger) advanceTodos(passAssistantText string) {
	l.todoChanged = false
	state := l.turnState()
	if l.hasList {
		l.list.ApplyMarkers(passAssistantText)
	} else if steps, err := plans.ExtractSteps(passAssistantText); err == nil && len(steps) > 0 {
		l.list = todos.New(l.goalText, steps)
		l.hasList = true
	}
	if l.turnState() != state {
		l.todoChanged = true
	}
}

// hasVerifiableWork reports whether the tracked list has anything a
// verification pass could check. Arming verification against an all-pending
// list spends a read-only pass confirming nothing. A done item with no write
// behind it is deliberately still verifiable: that is exactly the claim the
// verification pass exists to catch.
func (l *passLedger) hasVerifiableWork() bool {
	if !l.hasList {
		return false
	}
	for _, it := range l.list.Items {
		if it.Status == todos.StatusDone {
			return true
		}
	}
	return false
}

// noteWrites records one pass's harness-observed disk effect.
func (l *passLedger) noteWrites(out passOutcome) {
	l.passWrites = out.mutations
	if out.mutations > 0 {
		l.writes += out.mutations
		l.passesSinceWrite = 0
	} else {
		l.passesSinceWrite++
	}
	for _, p := range out.mutatedPaths {
		if len(l.touched) >= maxMutatedPaths {
			break
		}
		if !slices.Contains(l.touched, p) {
			l.touched = append(l.touched, p)
		}
	}
}

// noteWithheld records one pass's withheld count and the running tally of
// passes that ended all-withheld, so a tool outage is visible at the pass
// boundary rather than being collapsed into "no files changed".
func (l *passLedger) noteWithheld(out passOutcome) {
	l.passWithheld = out.withheld
	if out.withheld > 0 {
		l.withheldPasses++
	}
}

// everyPassWithheld reports whether every pass run so far ended with every
// tool result withheld. Combined with writes==0 it means the tool surface is
// broken, not that the model is dawdling.
func (l *passLedger) everyPassWithheld() bool {
	return l.passes > 0 && l.withheldPasses == l.passes
}

// stalledOnWrites reports whether the loop has gone long enough without a
// file change to stop asking politely. It is deliberately independent of the
// todo list: a model can keep a checklist moving with prose alone.
func (l *passLedger) stalledOnWrites() bool {
	return l.passesSinceWrite >= goalNoWritePasses
}

// nextStep names the step the loop expects to be worked on: the first
// in-progress item, else the first pending one. It is used to make the
// action directives concrete.
func (l *passLedger) nextStep() string {
	if !l.hasList {
		return ""
	}
	for _, it := range l.list.Items {
		if it.Status == todos.StatusActive {
			return it.Text
		}
	}
	for _, it := range l.list.Items {
		if it.Status == todos.StatusPending {
			return it.Text
		}
	}
	return ""
}

// noWriteDirective is the escalation for a run of passes that changed no
// file. It is the counterweight to the read-only pull of exploration: goal
// mode is not finished investigating, it is behind on writing.
func (l *passLedger) noWriteDirective() string {
	var b strings.Builder
	if l.writes == 0 {
		b.WriteString("No file has changed yet in this goal. Stop investigating and make the smallest correct edit that advances it now")
	} else {
		fmt.Fprintf(&b, "The last %d passes changed no files. Stop investigating and make the smallest correct edit that advances the goal now", l.passesSinceWrite)
	}
	if step := l.nextStep(); step != "" {
		fmt.Fprintf(&b, " — the next step is: %s", step)
	}
	b.WriteString(". Read the exact bytes you are about to edit, then edit them. If a real blocker prevents any edit, state the blocker in one line and say what you need.")
	if l.hasList {
		b.WriteString("\n\n" + l.list.Render())
	}
	return b.String()
}

// noWriteOrRepairDirective picks the boundary escalation for a pass that
// changed no file. When the pass also ended with every tool result withheld,
// the failure is the tool surface itself, not a model that stopped editing —
// the loop must not tell a model whose tools are broken to "stop investigating
// and edit now".
func (l *passLedger) noWriteOrRepairDirective() string {
	if l.passWithheld > 0 {
		return withheldRepairDirective
	}
	return l.noWriteDirective()
}

// goalFacts renders the harness-observed evidence the goal evaluator is shown
// beside the pass digest: pass number, file changes and the paths involved.
// Counts and paths are harness-computed facts — the same class the repo map
// is allowed to carry — never file contents.
func (l *passLedger) goalFacts() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Pass: %d\n", l.passes)
	fmt.Fprintf(&b, "Tool results withheld this pass: %d\n", l.passWithheld)
	fmt.Fprintf(&b, "Files changed this pass: %d\n", l.passWrites)
	fmt.Fprintf(&b, "Files changed so far in this goal: %d\n", l.writes)
	if len(l.touched) > 0 {
		fmt.Fprintf(&b, "Paths changed: %s\n", strings.Join(l.touched, ", "))
	} else {
		b.WriteString("Paths changed: none\n")
	}
	return b.String()
}

// notePartial records one no-progress PARTIAL verdict for stall detection.
// A todo-state transition resets the streak, so a progressing model can loop
// indefinitely; a stuck one receives a stronger progression directive and a
// reset of the streak, starting a new agentic evaluation loop instead of
// aborting.
func (l *passLedger) notePartial() bool {
	if l.todoChanged {
		l.partialStreak = 0
		return false
	}
	l.partialStreak++
	return l.partialStreak >= goalStallPartial
}

// partialDirectiveTurn selects the directive for a GOAL_PARTIAL step. A run
// of passes that changed no file outranks everything else: the loop is behind
// on writing, and a verification pass would spend another read-only pass.
// Verification is armed only when the tracked list has completed work to
// check and something has actually been written.
func (l *passLedger) partialDirectiveTurn() (body string, arm bool) {
	if l.stalledOnWrites() {
		return l.noWriteDirective(), false
	}
	if l.partialStreak%goalVerifyEvery == 0 && l.hasVerifiableWork() {
		return verificationDirective, true
	}
	return l.partialDirective(), false
}

// gateDirective is the body injected when GOAL_COMPLETE arrives before the
// verification gate has been satisfied. A goal that has changed no file is
// not verified by re-reading a repository it never touched, so that case gets
// the no-write directive instead.
func (l *passLedger) gateDirective() string {
	if l.writes == 0 {
		return l.noWriteOrRepairDirective()
	}
	return verificationDirective
}

// passLoop is the pass driver. Everything from sanitize through SealSystem
// stays in run (once per prompt); system is sealed exactly once and passed
// here as a value. Re-sealing would rotate nonces and invalidate
// already-sealed tool-result blocks (docs/resilience.md, "Turn retry and
// state invariants").
//
// Guard: when the session may not run a pass loop (subagent) or the mode is
// not goal, exactly one pass runs and today's max-iterations error is
// preserved verbatim. Only a top-level goal-mode prompt enters the loop.
func (s *Session) passLoop(ctx context.Context, pipe *rolemanager.Pipeline, system string, turns []run.Turn, modeDec rolemanager.ModeDecision, goalText, planContext, prompt string, streaming bool, emit func(Event)) (run.Result, error) {
	// Plan mode has its own pass loop: the harness contacts the model at a
	// pass boundary like goal mode does, but with the exploration context
	// gathered for the session and PLAN_* sentinels — never a goal definition
	// and never GOAL_* presentation.
	if s.allowPassLoop && modeDec.Mode == modes.ModePlan {
		res, err := s.planPassLoop(ctx, pipe, system, turns, planContext, streaming, emit)
		res = s.recordPlan(res, prompt, emit)
		return res, err
	}

	if !s.allowPassLoop || modeDec.Mode != modes.ModeGoal {
		out, turns, err := s.pass(ctx, pipe, system, turns, streaming, emit, modeDec.Mode)
		if err != nil {
			return run.Result{}, err
		}
		if !out.exhausted {
			return run.Result{Reply: out.reply, Usage: out.usage}, nil
		}

		// Reset-on-steer: an explore subagent whose iteration budget is spent
		// does not hard-fail if new steering arrived. The steering restarts
		// the budget and the subagent keeps investigating. Only when no new
		// steering exists does the budget exhaustion surface as an error.
		if s.exploreSubagent {
			for {
				if steer := s.drainSteer(ctx, pipe, emit); len(steer) > 0 {
					turns = append(turns, steer...)
					out, turns, err = s.pass(ctx, pipe, system, turns, streaming, emit, modeDec.Mode)
					if err != nil {
						return run.Result{}, err
					}
					if !out.exhausted {
						return run.Result{Reply: out.reply, Usage: out.usage}, nil
					}
					continue
				}
				break
			}
		}

		// Budget exhaustion is a turn boundary, not an error: inject a wrap-up
		// directive and grant continuation passes with fresh budgets.
		return s.agentContinuations(ctx, pipe, system, turns, streaming, emit, out, modeDec.Mode, prompt)
	}

	// maxPasses is an opt-in ceiling (0 = unbounded, the default). The loop's
	// own stall detectors are what normally stop it; this exists for CI and
	// for anyone who wants a hard bound on spend.
	maxPasses := s.settings.Resilience.MaxPassesOr()

	l := passLedger{goalText: goalText}
	gs := goals.NewGoalState(goalText)
	goalStart := time.Now()
	totalTokens := 0
	basePasses := 0
	if prior := s.turnPriorGoal; prior != nil {
		// A continuation resumes the goal in flight: the same id and running
		// totals, so passes, tokens, and time read as one goal, not two.
		gs.ID = prior.ID
		gs.CreatedAt = prior.CreatedAt
		gs.TokenBudget = prior.TokenBudget
		totalTokens = prior.TokensUsed
		basePasses = prior.Passes
		goalStart = goalStart.Add(-time.Duration(prior.TimeUsedSeconds) * time.Second)
	}
	turns = append(turns, directiveTurns(s.goalAckDirective())...)
	emitGoalState(emit, gs)
	for {
		// Cancellation is the only ceiling, and it must not read as an error:
		// a deliberate esc returns the partial result, never raw
		// context.Canceled.
		if err := ctx.Err(); err != nil {
			return run.Result{Passes: l.passes}, ErrPassLoopCancelled
		}

		if maxPasses > 0 && l.passes >= maxPasses {
			return run.Result{Passes: l.passes, GoalSentinel: rolemanager.GoalPartial},
				fmt.Errorf("goal pass loop stopped: max passes (%d) reached", maxPasses)
		}

		l.passes++
		emit(Event{Kind: EventPassKind, Pass: l.passes, Explored: l.surveyPending})
		l.surveyPending = false

		// Proactive compaction: at this structurally clean boundary, compact
		// when the estimated context exceeds the threshold — before an overflow
		// fails a pass and pays a retry backoff. compactBoundary is a no-op
		// below the threshold, so the common path is one cheap estimate.
		if compacted, ok := s.compactBoundary(ctx, pipe, turns); ok {
			turns = compacted
		}

		start := len(turns)
		out, turns, err := s.pass(ctx, pipe, system, turns, streaming, emit, modes.ModeGoal)
		if err != nil {
			// Cancellation must not read as an error: a deliberate esc returns
			// the partial result with ErrPassLoopCancelled, never a raw
			// context.Canceled that the transcript would print as an agent
			// error.
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return run.Result{Reply: out.lastText, Usage: out.usage, GoalSentinel: rolemanager.GoalPartial, Passes: l.passes}, ErrPassLoopCancelled
			}
			// A ClassOverflow escaping pass is caught once: compact at this
			// structurally clean boundary and re-run the pass. Any other error
			// is terminal — streamTurnRetry already owns the retry budget.
			if !l.overflowRetried && isOverflow(err) {
				l.overflowRetried = true
				if compacted, ok := s.compactBoundary(ctx, pipe, turns); ok {
					turns = compacted
					out, turns, err = s.pass(ctx, pipe, system, turns, streaming, emit, modes.ModeGoal)
				}
			}
			if err != nil {
				return run.Result{Passes: l.passes}, maybeCompact(err)
			}
		}
		if l.verificationArmed {
			l.verificationPasses++
			l.verificationArmed = false
		}

		// Record what the pass did to disk before anything else looks at it:
		// the write ledger is what the directives and the verification gate
		// key off.
		l.noteWrites(out)
		l.noteWithheld(out)

		// A goal whose every pass ended with every tool result withheld has a
		// broken tool surface, not a lazy model. Terminate with the tool
		// failure named rather than granting unbounded passes against a
		// resolver that cannot answer.
		if l.writes == 0 && l.everyPassWithheld() {
			return run.Result{Passes: l.passes}, fmt.Errorf("goal pass loop stopped: every pass ended with all tool results withheld; re-check the tool path resolver and provider")
		}

		// Maintain the shared todo list from assistant text only, then tell
		// the TUI when it changed so it can render and persist it.
		l.advanceTodos(out.text)
		if out.updatePlan != nil {
			if !l.hasList {
				l.list = todos.New(l.goalText, nil)
				l.hasList = true
			}
			l.list.Adopt(out.updatePlan.Items)
			list := l.list
			emit(Event{Kind: EventTodosKind, Todos: &list})
		}
		if l.todoChanged && l.hasList {
			list := l.list
			emit(Event{Kind: EventTodosKind, Todos: &list})
		}
		totalTokens += out.spent
		gs.Passes = basePasses + l.passes
		gs.TokensUsed = totalTokens
		gs.TimeUsedSeconds = int(time.Since(goalStart).Seconds())
		emitGoalState(emit, gs)

		if !out.exhausted {
			// Natural exit: a no-tool-call reply is a claim of completion, not
			// proof. Re-check the goal sentinel against the reply text itself
			// once before trusting it. A non-complete verdict injects the
			// continuation directive and loops; the existing stall detectors
			// still bound a loop that is not advancing.
			sentinel, stop, evalErr := s.evaluateGoalPass(ctx, pipe, &l, sanitize.Sanitize(out.reply), emit)
			if evalErr != nil {
				return run.Result{Passes: l.passes}, evalErr
			}
			if stop {
				return s.goalReport(ctx, system, withReply(turns, out.reply), streaming, emit, sentinel,
					run.Result{Reply: out.reply, Usage: out.usage, GoalSentinel: sentinel, Passes: l.passes}), nil
			}
			if sentinel == rolemanager.GoalComplete {
				if l.verificationPasses == 0 {
					l.verificationArmed = true
					turns = append(turns, directiveTurns(l.gateDirective())...)
					continue
				}
				if l.hasList {
					l.list.MarkAllDone()
					list := l.list
					emit(Event{Kind: EventTodosKind, Todos: &list})
				}
				gs.Status = string(goals.StatusComplete)
				emitGoalState(emit, gs)
				return s.goalReport(ctx, system, withReply(turns, out.reply), streaming, emit, sentinel,
					run.Result{Reply: out.reply, Usage: out.usage, GoalSentinel: sentinel, Passes: l.passes}), nil
			}
			if l.notePartial() {
				l.partialStreak = 0
				turns = append(turns, directiveTurns(l.progressionDirective())...)
				continue
			}
			if l.stalledOnWrites() {
				turns = append(turns, directiveTurns(l.noWriteOrRepairDirective())...)
				continue
			}
			turns = append(turns, directiveTurns(continuationDirective)...)
			continue
		}

		// Steering outranks the evaluator: explicit user intent beats a
		// classifier, and skipping the call saves a model round-trip.
		if steer := s.drainSteer(ctx, pipe, emit); len(steer) > 0 {
			turns = append(turns, steer...)
			continue
		}

		// A zero-productive pass burns its budget without doing any work
		// (truncation repair, rejected arguments, withheld calls). It produces
		// no evidence, so it does not reach the evaluator — but one such pass
		// is repairable, and discarding a whole goal over a rejected argument
		// shape throws away the passes that did work. Inject the repair
		// directive and try once more; a second consecutive empty pass is a
		// model that cannot call a tool, and the loop stops.
		if out.productive == 0 {
			l.unproductivePasses++
			if l.unproductivePasses >= maxUnproductivePasses {
				// A goal that has already changed files keeps its work: the
				// edits are on disk either way, and returning an error would
				// throw away the reply that describes them. A goal that has
				// written nothing has nothing to return, so the failure is
				// surfaced as one.
				if l.writes > 0 {
					emit(Event{Kind: EventWarningKind, Warning: fmt.Sprintf("pass %d executed no tools; returning the work so far", l.passes)})
					return s.goalReport(ctx, system, turns, streaming, emit, rolemanager.GoalPartial,
						run.Result{Reply: out.lastText, Usage: out.usage, GoalSentinel: rolemanager.GoalPartial, Passes: l.passes}), nil
				}
				return run.Result{Passes: l.passes}, fmt.Errorf("goal pass loop stopped: pass %d executed no tools", l.passes)
			}
			emit(Event{Kind: EventWarningKind, Warning: fmt.Sprintf("pass %d executed no tools; asking for corrected tool calls", l.passes)})
			turns = append(turns, directiveTurns(toolRepairDirective)...)
			continue
		}
		l.unproductivePasses = 0

		// The evaluator is the only model call at a pass boundary. An earlier
		// version also asked the main model for a per-pass progress report;
		// it cost a second full-context turn every pass, carried its own
		// prose forward, and taught the model that a pass's deliverable is a
		// report. The pass's own assistant text is the report, and the next
		// iteration starts instead.
		sentinel, stop, evalErr := s.evaluateGoalPass(ctx, pipe, &l, sanitize.Sanitize(evidenceDigest(turns[start:])), emit)
		if evalErr != nil {
			// Transport failure: terminal. The verdict is unknown, and an
			// unknown verdict must not grant compute.
			return run.Result{Passes: l.passes}, evalErr
		}
		if stop {
			return s.goalReport(ctx, system, turns, streaming, emit, sentinel,
				run.Result{Reply: out.lastText, Usage: out.usage, GoalSentinel: sentinel, Passes: l.passes}), nil
		}

		switch sentinel {
		case rolemanager.GoalNotStarted:
			// A forced explore must name edit targets, not re-ask the
			// original question: a goal-mode prompt usually has no
			// @references, so the ordinary plan would just repeat it. It runs
			// at most once per goal — a second survey buys reading, and
			// reading is not what a not-started goal is short of.
			if !l.surveyedOnce {
				if survey := s.goalSurveyTurns(ctx, l.goalText, pipe, emit); len(survey) > 0 {
					turns = append(turns, survey...)
					turns = append(turns, run.Turn{Role: "assistant", Content: rolemanager.SummaryAck})
					l.surveyPending = true
				}
				l.surveyedOnce = true
			}
			turns = append(turns, directiveTurns(planDirective)...)

		case rolemanager.GoalPartial:
			if l.notePartial() {
				// Stall detected. Provide session context, reset progression
				// tracking, and start a new agentic evaluation loop rather
				// than aborting.
				l.partialStreak = 0
				turns = append(turns, directiveTurns(l.progressionDirective())...)
				continue
			}
			body, arm := l.partialDirectiveTurn()
			if arm {
				l.verificationArmed = true
			}
			turns = append(turns, directiveTurns(body)...)

		case rolemanager.GoalComplete:
			if l.verificationPasses == 0 {
				// Verification gate: GOAL_COMPLETE is only accepted after at
				// least one verification pass ran in this prompt. This is
				// harness logic, not model logic — the model cannot talk its
				// way past it. Downgrade to PARTIAL and run exactly one.
				if l.notePartial() {
					// Stall detected even though the model claims completion.
					// Provide session context, reset progression tracking, and
					// start a new agentic evaluation loop rather than aborting.
					l.partialStreak = 0
					turns = append(turns, directiveTurns(l.progressionDirective())...)
					continue
				}
				l.verificationArmed = true
				turns = append(turns, directiveTurns(l.gateDirective())...)
				continue
			}
			// Accepted: mark the todo list complete and return.
			if l.hasList {
				l.list.MarkAllDone()
				list := l.list
				emit(Event{Kind: EventTodosKind, Todos: &list})
			}
			gs.Status = string(goals.StatusComplete)
			emitGoalState(emit, gs)
			return s.goalReport(ctx, system, turns, streaming, emit, sentinel, run.Result{
				Reply:        out.lastText,
				Usage:        out.usage,
				GoalSentinel: sentinel,
				Passes:       l.passes,
			}), nil

		default:
			// Unreachable: ParseGoalSentinel accepts only the three sentinels,
			// and malformed input resolves to GOAL_PARTIAL.
			return run.Result{Passes: l.passes}, fmt.Errorf("goal pass loop: unknown verdict %q", sentinel)
		}
	}
}

// agentContinuations runs the budget-exhaustion wrap-up passes for agent and
// plan mode (and subagents). Each exhausted pass injects a sealed continuation
// directive and grants one more bounded pass with a fresh tool budget: a pass
// that keeps emitting tool calls loops again; a pass that ends with text only
// is the turn's normal answer. Capped by resilience.max_passes (0 falls back
// to defaultAgentContinuations). Reaching the cap returns the last assistant
// text, never an error.
func (s *Session) agentContinuations(ctx context.Context, pipe *rolemanager.Pipeline, system string, turns []run.Turn, streaming bool, emit func(Event), out passOutcome, mode modes.Mode, userPrompt string) (run.Result, error) {
	maxCont := s.settings.Resilience.MaxPassesOr()
	if maxCont <= 0 {
		maxCont = defaultAgentContinuations
	}
	continuations := 0
	mutations := out.mutations
	for continuations < maxCont {
		if steer := s.drainSteer(ctx, pipe, emit); len(steer) > 0 {
			turns = append(turns, steer...)
			var err error
			out, turns, err = s.pass(ctx, pipe, system, turns, streaming, emit, mode)
			if err != nil {
				return run.Result{}, err
			}
			if !out.exhausted {
				return run.Result{Reply: out.reply, Usage: out.usage, Passes: continuations}, nil
			}
			continue
		}
		continuations++
		s.traceRecord("continuation", "", "", fmt.Sprintf("continuation=%d cap=%d", continuations, maxCont), continuations)
		emit(Event{Kind: EventContinuationKind, Pass: continuations, MaxPasses: maxCont})
		// An agent turn used to die at the context window: only the goal and
		// plan loops compacted. Compact here too, then restate the request
		// so the summary cannot lose what the user asked for.
		if compacted, ok := s.compactBoundary(ctx, pipe, turns); ok {
			turns = compacted
			if strings.TrimSpace(userPrompt) != "" {
				turns = append(turns, run.Turn{Role: "user", Content: userPrompt})
			}
			emit(Event{Kind: EventWarningKind, Warning: "context compacted to keep the turn going"})
		}
		directive := continuationDirective
		if mode == modes.ModeAgent && !s.turnReadOnly && mutations == 0 {
			directive = agentEditNudge + " " + continuationDirective
		}
		turns = append(turns, directiveTurns(directive)...)
		var err error
		out, turns, err = s.pass(ctx, pipe, system, turns, streaming, emit, mode)
		if err != nil {
			return run.Result{}, err
		}
		mutations += out.mutations
		if !out.exhausted {
			return run.Result{Reply: out.reply, Usage: out.usage, Passes: continuations}, nil
		}
		if out.productive == 0 {
			break
		}
	}
	emit(Event{Kind: EventWarningKind, Warning: fmt.Sprintf("turn budget reached; returning the work so far after %d continuation pass(es)", continuations)})
	return run.Result{Reply: out.lastText, Usage: out.usage, Passes: continuations}, nil
}

// evaluateGoalPass runs the goal evaluator for one pass boundary and folds the
// malformed-reply and transport-failure bookkeeping into the ledger.
//
// The returns are: the verdict (always usable — a malformed reply or a
// transport failure fails closed to GOAL_PARTIAL); stop, meaning the
// evaluator has failed often enough that the loop must end gracefully with
// the work so far; and an error, which is no longer produced here but is
// kept in the signature so a caller can still stop on an unknown verdict if
// one is ever reintroduced. A garbled classifier token is not a reason to
// throw away a long run, and neither is a provider error at a pass boundary:
// both count toward a bounded streak, and the streak limit returns the work
// rather than an error.
func (s *Session) evaluateGoalPass(ctx context.Context, pipe *rolemanager.Pipeline, l *passLedger, evidence string, emit func(Event)) (rolemanager.GoalSentinel, bool, error) {
	sentinel, err := rolemanager.EvaluateGoal(ctx, pipe.Classifier, rolemanager.GoalEvalInput{
		Goal:     l.goalText,
		Todos:    l.list.Render(),
		Facts:    l.goalFacts(),
		Evidence: evidence,
	})
	if err == nil {
		l.malformedStreak = 0
		l.evalErrorStreak = 0
		emit(Event{Kind: EventGoalEvalKind, Pass: l.passes, GoalSentinel: sentinel})
		return sentinel, false, nil
	}
	if !errors.Is(err, rolemanager.ErrMalformedGoalEval) {
		// A transport failure leaves the verdict unknown, but discarding the
		// whole run at a pass boundary over one provider error is worse than
		// continuing with a fail-closed GOAL_PARTIAL verdict — the same
		// reasoning that treats a malformed reply as non-terminal. Count
		// consecutive failures so a permanently unreachable evaluator still
		// stops the loop gracefully with the work so far instead of granting
		// unbounded passes.
		l.evalErrorStreak++
		emit(Event{Kind: EventGoalEvalKind, Pass: l.passes, GoalSentinel: rolemanager.GoalPartial})
		if l.evalErrorStreak >= maxGoalEvalErrors {
			emit(Event{Kind: EventWarningKind, Warning: fmt.Sprintf("goal evaluator failed %d times in a row (%v); stopping the goal loop and returning the work so far", l.evalErrorStreak, err)})
			return rolemanager.GoalPartial, true, nil
		}
		emit(Event{Kind: EventWarningKind, Warning: fmt.Sprintf("goal evaluator transport error: %v; continuing as %s", err, rolemanager.GoalPartial.Label())})
		return rolemanager.GoalPartial, false, nil
	}
	// Malformed output fails closed to GOAL_PARTIAL (one garbled reply is
	// noise, and the evaluator was already re-asked once with the exact
	// syntax it broke); two in a row is a broken evaluator. Reaching the
	// evaluator at all — even with a garbled reply — resets the transport
	// failure streak.
	l.evalErrorStreak = 0
	l.malformedStreak++
	emit(Event{Kind: EventGoalEvalKind, Pass: l.passes, GoalSentinel: sentinel, Malformed: true})
	if l.malformedStreak >= maxMalformedEvals {
		emit(Event{Kind: EventWarningKind, Warning: fmt.Sprintf("goal evaluator returned %d malformed replies in a row; stopping the goal loop and returning the work so far", l.malformedStreak)})
		return sentinel, true, nil
	}
	return sentinel, false, nil
}

// partialDirective builds the continuation instruction for a PARTIAL verdict:
// the current list state when one exists, otherwise the instruction to start
// tracking one.
func (l *passLedger) partialDirective() string {
	if l.hasList {
		body := "Continue."
		if step := l.nextStep(); step != "" {
			body += " The next action is: " + step + ". Carry it out with an edit in this pass."
		} else {
			body += " Carry out the next action with an edit in this pass."
		}
		return body + "\n\n" + l.list.Render() +
			"\n\nMark steps complete with update_plan, or with [DONE:n] in your reply, as you finish them."
	}
	return "The goal is partially complete and no step list is tracked yet. Call update_plan with the steps you will execute, then carry out the next one with an edit in this pass."
}

// progressionDirective builds the directive injected when the pass loop has
// not seen todo progress for goalStallPartial consecutive passes. It provides
// session context and asks the model to identify the single most concrete next
// step, creating a new agentic evaluation loop rather than aborting.
func (l *passLedger) progressionDirective() string {
	if l.hasList {
		return "Progress has stalled — the step list has not advanced for several passes. Execute the single most concrete next step now, as an edit; review the conversation history above only as far as that step needs. If you are blocked, state the blocker explicitly.\n\n" +
			l.list.Render() +
			"\n\nMark steps complete with update_plan, or with [DONE:n] in your reply, as you finish them."
	}
	return "Progress has stalled and no step list is tracked yet. Call update_plan with the steps you will execute, then carry out the first one as an edit in this pass. If you are blocked, state the blocker explicitly."
}

// Final report directives. A goal pass loop ends on an evaluator verdict, and
// the pass that earned it usually ends mid-work — its last words are a tool
// narration, not an account of the goal. Every non-error end of the loop
// therefore asks the model once more, with no tool work allowed, for the
// report the user reads.
const (
	goalReportDirective     = "The goal is complete and the harness has verified it. Do not call any tools. Write the final report for the user now: what was changed (each file and the substance of the change), how it was verified (the commands run and their outcome), and anything left open or worth following up. Be concise and factual, and report only work that is visible in this conversation."
	goalStopReportDirective = "The goal loop has stopped before the goal was confirmed complete. Do not call any tools. Write a report for the user now: what was changed (each file and the substance of the change), what was verified and how, and what remains unfinished and why. Be concise and factual, and report only work that is visible in this conversation."
)

// reportDirective picks the final report directive for the sentinel the loop
// ended on.
func reportDirective(sentinel rolemanager.GoalSentinel) string {
	if sentinel == rolemanager.GoalComplete {
		return goalReportDirective
	}
	return goalStopReportDirective
}

// withReply appends a natural-exit pass's closing reply to turns. pass returns
// on a text-only reply without appending it, so the report turn would
// otherwise follow the previous user turn with no assistant turn between.
func withReply(turns []run.Turn, reply string) []run.Turn {
	if strings.TrimSpace(reply) == "" {
		return turns
	}
	return append(turns, run.Turn{Role: "assistant", Content: reply})
}

// goalReport runs the final report turn of a goal pass loop and returns res
// with the report as its reply. It is one provider turn: any tool call the
// model makes anyway is ignored, never executed. The tool surface is still
// advertised so the provider accepts the tool blocks in the history. A report
// that fails or comes back empty never costs the goal: res is returned as it
// was, with a warning naming the failure.
func (s *Session) goalReport(ctx context.Context, system string, turns []run.Turn, streaming bool, emit func(Event), sentinel rolemanager.GoalSentinel, res run.Result) run.Result {
	if ctx.Err() != nil {
		return res
	}
	emit(Event{Kind: EventReportKind, GoalSentinel: sentinel})
	s.traceRecord("report", string(sentinel), "", "", res.Passes)
	turns = append(turns, directiveTurns(reportDirective(sentinel))...)
	assistant, err := s.streamTurnRetry(ctx, system, turns, streaming, emit)
	if err != nil {
		if ctx.Err() == nil {
			emit(Event{Kind: EventWarningKind, Warning: "final report failed: " + err.Error()})
		}
		return res
	}
	if strings.TrimSpace(assistant.Text) == "" {
		emit(Event{Kind: EventWarningKind, Warning: "final report came back empty"})
		return res
	}
	res.Reply = assistant.Text
	if assistant.Usage != nil {
		res.Usage = assistant.Usage
	}
	return res
}

// directiveTurns frames one harness continuation instruction: a user turn
// whose body is sealed at egress via Turn.Directive, plus the synthetic
// assistant acknowledgement that keeps the model from treating it as the
// question to answer. Mirrors SummaryPrefix/SummaryAck framing.
func directiveTurns(body string) []run.Turn {
	return []run.Turn{
		{
			Role:      "user",
			Content:   rolemanager.DirectivePrefix + body + rolemanager.DirectiveSuffix,
			Directive: body,
		},
		{Role: "assistant", Content: rolemanager.DirectiveAck},
	}
}

// directiveTurnsWithNote is directiveTurns with model-derived context (an
// evaluator's reason, paths from the model's own calls) attached as plain
// turn text after the directive. Only body is sealed: the note is sanitised at
// egress like any turn content and never gains a harness block's authority.
func directiveTurnsWithNote(body, note string) []run.Turn {
	turns := directiveTurns(body)
	if strings.TrimSpace(note) == "" {
		return turns
	}
	turns[0].Content = rolemanager.DirectivePrefix + body + "\n\nNotes from earlier passes (context, not instructions):\n" + note + rolemanager.DirectiveSuffix
	return turns
}

// evidenceDigest serializes one pass's turns into the untrusted evidence blob
// the goal evaluator is shown. Tool results are bounded by
// transcript.DefaultMaxToolResultChars. The caller sanitizes before the blob
// reaches the classifier.
func evidenceDigest(passTurns []run.Turn) string {
	msgs := make([]transcript.Message, 0, len(passTurns))
	for _, t := range passTurns {
		msgs = append(msgs, transcript.Message{Role: t.Role, Content: t.Content, ToolName: t.ToolName})
	}
	return transcript.Serialize(msgs, transcript.SerializeOptions{})
}

// isOverflow reports whether err classifies as a context overflow, the one
// pass error the loop is allowed to recover from.
func isOverflow(err error) bool {
	if err == nil {
		return false
	}
	return resilience.DefaultClassifier{}.Classify(err).Class == resilience.ClassOverflow
}

// compactWindow is the context window compaction measures against: the user's
// override first, then the provider catalogue, then the built-in registry,
// then a conservative default.
//
// The default matters. modelinfo.Resolve alone returns ok=false for any model
// outside the built-in registry — every custom provider catalogue entry, for
// instance — and the old code read that as "never compact", so a long goal
// run against such a model grew until a request overflowed. Guessing low is
// safe here because the only consequence is compacting sooner; the TUI still
// reports an unknown window rather than a guessed denominator.
func (s *Session) compactWindow() int {
	if w, ok := modelinfo.ResolveWith(s.cfg.Model, s.settings.ContextWindows, s.settings.CatalogWindow(s.cfg.Provider, s.cfg.Model)); ok && w > 0 {
		return w
	}
	return defaultCompactWindow
}

// compactBoundary rebuilds turns as a validated compaction summary when the
// estimated context exceeds compactThresholdPct of the model window. It runs
// only at pass boundaries: mid-pass, turns may hold assistant tool_calls
// whose matching tool turns are not yet appended, and truncating there would
// orphan tool_call_ids (providers reject the payload).
//
// The summary is model output derived from tool results, so it is admitted
// through the Role Manager before re-injection — the same fail-closed rule as
// steering, and a deliberate fix over the TUI /compact path, which does not
// admit. Any failure skips compaction for this boundary; the next boundary
// retries. Returns ok=false when compaction was not attempted or did not
// produce a usable summary.
func (s *Session) compactBoundary(ctx context.Context, pipe *rolemanager.Pipeline, turns []run.Turn) ([]run.Turn, bool) {
	window := s.compactWindow()
	msgs := make([]transcript.Message, 0, len(turns))
	for _, t := range turns {
		msgs = append(msgs, transcript.Message{Role: t.Role, Content: t.Content, ToolName: t.ToolName})
	}
	est := transcript.EstimateContext(msgs)
	if est.Tokens*100 < window*compactThresholdPct {
		return nil, false
	}
	conv := transcript.Serialize(msgs, transcript.SerializeOptions{})
	raw, err := pipe.Classifier.Classify(ctx, rolemanager.BuildCompactionPayload(conv, s.settings.ClassifierCavemanEnabled()))
	if err != nil {
		return nil, false
	}
	summary, err := rolemanager.ValidateSummary(raw)
	if err != nil {
		return nil, false
	}
	dec, err := pipe.Admit(ctx, summary, "summary", s.live.Policy())
	if err != nil || dec.Action != rolemanager.ActionProceed {
		return nil, false
	}
	return []run.Turn{
		{Role: "user", Content: rolemanager.SummaryPrefix + dec.Content + rolemanager.SummarySuffix},
		{Role: "assistant", Content: rolemanager.SummaryAck},
	}, true
}

// emitGoalState sends a copy of the goal state. The loop keeps mutating its
// own gs after the send and the TUI reads the event asynchronously, so a
// shared pointer would be a data race.
func emitGoalState(emit func(Event), gs goals.GoalState) {
	cp := gs
	emit(Event{Kind: EventGoalStateKind, GoalState: &cp})
}
