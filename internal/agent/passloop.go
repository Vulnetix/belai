package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/signet/internal/goals"
	"github.com/vulnetix/signet/internal/modelinfo"
	"github.com/vulnetix/signet/internal/modes"
	"github.com/vulnetix/signet/internal/plans"
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
	// compactThresholdPct: compact at the pass boundary when the estimated
	// context exceeds this share of the model window.
	compactThresholdPct = 70
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
	planDirective         = "The goal has not started yet. Write a planning todo list under a 'Plan:' header (numbered steps), then begin the first step. Mark each step complete with [DONE:n] in your reply as you finish it."
	verificationDirective = "Before doing any further work, verify the completed items in the todo list against the files on disk (read-only). Confirm each marked-done item is actually true; if one is not, correct the list and the work. Only continue new work after the check."
	// continuationDirective is injected when a bounded pass spends its whole
	// iteration budget. Budget exhaustion is a turn boundary, not a failure.
	continuationDirective = "The tool budget for this turn was reached. Report the work done so far and what remains. If more tool calls are needed to finish the work, make them now; otherwise give the final answer."
)

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
	// surveyedLastPass: the pass that just ended ran on survey findings, so a
	// repeat GOAL_NOT_STARTED gets the planning instruction without a second
	// survey.
	surveyedLastPass bool

	// verificationArmed: the pass about to run carries the verification
	// directive; verificationPasses counts finished verification passes.
	verificationArmed  bool
	verificationPasses int

	// partialStreak counts consecutive no-progress PARTIAL verdicts; a todo
	// transition resets it. malformedStreak counts consecutive malformed
	// evaluator replies; a clean reply resets it.
	partialStreak   int
	malformedStreak int

	// overflowRetried: a ClassOverflow escaping pass is caught once (compact,
	// re-run the pass); a second overflow is terminal.
	overflowRetried bool
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

// passLoop is the pass driver. Everything from sanitize through SealSystem
// stays in run (once per prompt); system is sealed exactly once and passed
// here as a value. Re-sealing would rotate nonces and invalidate
// already-sealed tool-result blocks (docs/resilience.md, "Turn retry and
// state invariants").
//
// Guard: when the session may not run a pass loop (subagent) or the mode is
// not goal, exactly one pass runs and today's max-iterations error is
// preserved verbatim. Only a top-level goal-mode prompt enters the loop.
func (s *Session) passLoop(ctx context.Context, pipe *rolemanager.Pipeline, system string, turns []run.Turn, modeDec rolemanager.ModeDecision, goalText string, streaming bool, emit func(Event)) (run.Result, error) {
	if !s.allowPassLoop || modeDec.Mode != modes.ModeGoal {
		out, turns, err := s.pass(ctx, pipe, system, turns, streaming, emit)
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
					out, turns, err = s.pass(ctx, pipe, system, turns, streaming, emit)
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
		return s.agentContinuations(ctx, pipe, system, turns, streaming, emit, out)
	}

	// maxPasses is an opt-in ceiling (0 = unbounded, the default). The loop's
	// own stall detectors are what normally stop it; this exists for CI and
	// for anyone who wants a hard bound on spend.
	maxPasses := s.settings.Resilience.MaxPassesOr()

	l := passLedger{goalText: goalText}
	gs := goals.NewGoalState(goalText)
	goalStart := time.Now()
	totalTokens := 0
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
		out, turns, err := s.pass(ctx, pipe, system, turns, streaming, emit)
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
					out, turns, err = s.pass(ctx, pipe, system, turns, streaming, emit)
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

		// Maintain the shared todo list from assistant text only, then tell
		// the TUI when it changed so it can render and persist it.
		l.advanceTodos(out.text)
		if l.todoChanged && l.hasList {
			list := l.list
			emit(Event{Kind: EventTodosKind, Todos: &list})
		}
		if out.usage != nil {
			totalTokens += out.usage.Total()
		}
		gs.Passes = l.passes
		gs.TokensUsed = totalTokens
		gs.TimeUsedSeconds = int(time.Since(goalStart).Seconds())
		emit(Event{Kind: EventGoalStateKind, GoalState: &gs})

		if !out.exhausted {
			// Natural exit: a no-tool-call reply is a claim of completion, not
			// proof. Re-check the goal sentinel against the reply text itself
			// once before trusting it. A non-complete verdict injects the
			// continuation directive and loops; the existing stall detectors
			// still bound a loop that is not advancing.
			sentinel, evalErr := rolemanager.EvaluateGoal(ctx, pipe.Classifier, rolemanager.GoalEvalInput{
				Goal:     l.goalText,
				Todos:    l.list.Render(),
				Evidence: sanitize.Sanitize(out.reply),
			})
			if evalErr != nil {
				if !errors.Is(evalErr, rolemanager.ErrMalformedGoalEval) {
					return run.Result{Passes: l.passes}, evalErr
				}
				l.malformedStreak++
				if l.malformedStreak >= 2 {
					return run.Result{Passes: l.passes, GoalSentinel: sentinel},
						fmt.Errorf("goal pass loop stopped: %d consecutive malformed evaluator replies", l.malformedStreak)
				}
			}
			emit(Event{Kind: EventGoalEvalKind, Pass: l.passes, GoalSentinel: sentinel})
			if sentinel == rolemanager.GoalComplete {
				if l.verificationPasses == 0 {
					l.verificationArmed = true
					turns = append(turns, directiveTurns(verificationDirective)...)
					continue
				}
				if l.hasList {
					l.list.MarkAllDone()
					list := l.list
					emit(Event{Kind: EventTodosKind, Todos: &list})
				}
				gs.Status = string(goals.StatusComplete)
				emit(Event{Kind: EventGoalStateKind, GoalState: &gs})
				return run.Result{Reply: out.reply, Usage: out.usage, GoalSentinel: sentinel, Passes: l.passes}, nil
			}
			if l.notePartial() {
				l.partialStreak = 0
				turns = append(turns, directiveTurns(l.progressionDirective())...)
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
		// (truncation repair, withheld calls). It produces no evidence and
		// must not buy another pass.
		if out.productive == 0 {
			return run.Result{Passes: l.passes}, fmt.Errorf("goal pass loop stopped: pass %d executed no tools", l.passes)
		}

		// The goal evaluator and the progress-report turn run concurrently
		// against the same pass evidence. The sentinel alone drives control
		// flow; the report is assistant text appended to turns and shown in
		// the TUI.
		var sentinel rolemanager.GoalSentinel
		var evalErr error
		var report string
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			sentinel, evalErr = rolemanager.EvaluateGoal(ctx, pipe.Classifier, rolemanager.GoalEvalInput{
				Goal:     l.goalText,
				Todos:    l.list.Render(),
				Evidence: sanitize.Sanitize(evidenceDigest(turns[start:])),
			})
		}()
		go func() {
			defer wg.Done()
			report = s.progressReport(ctx, system, turns, l.goalText, streaming, emit)
		}()
		wg.Wait()
		if report != "" {
			turns = append(turns, run.Turn{Role: "assistant", Content: report})
		}
		if evalErr != nil {
			if !errors.Is(evalErr, rolemanager.ErrMalformedGoalEval) {
				// Transport failure: terminal. The verdict is unknown, and an
				// unknown verdict must not grant compute.
				return run.Result{Passes: l.passes}, evalErr
			}
			// Malformed output fails closed to GOAL_PARTIAL (one garbled reply
			// is noise); two in a row is a broken evaluator.
			l.malformedStreak++
			emit(Event{Kind: EventGoalEvalKind, Pass: l.passes, GoalSentinel: sentinel})
			if l.malformedStreak >= 2 {
				return run.Result{Passes: l.passes, GoalSentinel: sentinel},
					fmt.Errorf("goal pass loop stopped: %d consecutive malformed evaluator replies", l.malformedStreak)
			}
		} else {
			l.malformedStreak = 0
			emit(Event{Kind: EventGoalEvalKind, Pass: l.passes, GoalSentinel: sentinel})
		}

		switch sentinel {
		case rolemanager.GoalNotStarted:
			if !l.surveyedLastPass {
				// A forced explore must survey the repository, not re-ask the
				// original question: a goal-mode prompt usually has no
				// @references, so the ordinary plan would just repeat it.
				if survey := s.goalSurveyTurns(ctx, l.goalText); len(survey) > 0 {
					turns = append(turns, survey...)
					turns = append(turns, run.Turn{Role: "assistant", Content: rolemanager.SummaryAck})
					l.surveyPending = true
				}
				l.surveyedLastPass = true
			} else {
				l.surveyedLastPass = false
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
			if l.partialStreak%goalVerifyEvery == 0 {
				// Every Nth no-progress partial pass is a verification pass.
				l.verificationArmed = true
				turns = append(turns, directiveTurns(verificationDirective)...)
			} else {
				turns = append(turns, directiveTurns(l.partialDirective())...)
			}

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
				turns = append(turns, directiveTurns(verificationDirective)...)
				continue
			}
			// Accepted: mark the todo list complete and return.
			if l.hasList {
				l.list.MarkAllDone()
				list := l.list
				emit(Event{Kind: EventTodosKind, Todos: &list})
			}
			gs.Status = string(goals.StatusComplete)
			emit(Event{Kind: EventGoalStateKind, GoalState: &gs})
			return run.Result{
				Reply:        out.lastText,
				Usage:        out.usage,
				GoalSentinel: sentinel,
				Passes:       l.passes,
			}, nil

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
func (s *Session) agentContinuations(ctx context.Context, pipe *rolemanager.Pipeline, system string, turns []run.Turn, streaming bool, emit func(Event), out passOutcome) (run.Result, error) {
	maxCont := s.settings.Resilience.MaxPassesOr()
	if maxCont <= 0 {
		maxCont = defaultAgentContinuations
	}
	continuations := 0
	for continuations < maxCont {
		if steer := s.drainSteer(ctx, pipe, emit); len(steer) > 0 {
			turns = append(turns, steer...)
			var err error
			out, turns, err = s.pass(ctx, pipe, system, turns, streaming, emit)
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
		turns = append(turns, directiveTurns(continuationDirective)...)
		var err error
		out, turns, err = s.pass(ctx, pipe, system, turns, streaming, emit)
		if err != nil {
			return run.Result{}, err
		}
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

// progressReport asks the main model, at a pass boundary, for a concise
// work-done-this-pass / next-actions report. It is a normal model turn (never
// a classifier payload) and runs beside the evaluator; any tool calls it emits
// are ignored — only its text is returned.
func (s *Session) progressReport(ctx context.Context, system string, turns []run.Turn, goalText string, streaming bool, emit func(Event)) string {
	prompt := run.Turn{Role: "user", Content: "Report concisely for the harness: the work completed this pass and the concrete next actions. Goal: " + goalText}
	rp := append(append([]run.Turn{}, turns...), prompt)
	assistant, err := s.streamTurnRetry(ctx, system, rp, streaming, emit)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(assistant.Text)
}

// partialDirective builds the continuation instruction for a PARTIAL verdict:
// the current list state when one exists, otherwise the instruction to start
// tracking one.
func (l *passLedger) partialDirective() string {
	if l.hasList {
		return "The goal is partially complete. Continue from the current todo list state:\n\n" +
			l.list.Render() +
			"\n\nMark steps complete with [DONE:n] in your reply as you finish them."
	}
	return "The goal is partially complete but no todo list is tracked yet. Write a planning todo list under a 'Plan:' header (numbered steps), then continue. Mark each step complete with [DONE:n] in your reply as you finish it."
}

// progressionDirective builds the directive injected when the pass loop has
// not seen todo progress for goalStallPartial consecutive passes. It provides
// session context and asks the model to identify the single most concrete next
// step, creating a new agentic evaluation loop rather than aborting.
func (l *passLedger) progressionDirective() string {
	if l.hasList {
		return "The goal is partially complete but progress has stalled — the todo list has not advanced for several passes. Review the conversation history above and the current todo list state, identify the single most concrete next step that will move the goal forward, and execute it. If you are blocked, state the blocker explicitly.\n\n" +
			l.list.Render() +
			"\n\nMark steps complete with [DONE:n] in your reply as you finish them."
	}
	return "The goal is partially complete but progress has stalled — no todo list is tracked yet. Review the conversation history above, write a planning todo list under a 'Plan:' header (numbered steps), then execute the first step. If you are blocked, state the blocker explicitly. Mark each step complete with [DONE:n] in your reply as you finish it."
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
	window, ok := modelinfo.Resolve(s.cfg.Model, s.settings.ContextWindows)
	if !ok || window <= 0 {
		return nil, false
	}
	msgs := make([]transcript.Message, 0, len(turns))
	for _, t := range turns {
		msgs = append(msgs, transcript.Message{Role: t.Role, Content: t.Content, ToolName: t.ToolName})
	}
	est := transcript.EstimateContext(msgs)
	if est.Tokens*100 < window*compactThresholdPct {
		return nil, false
	}
	conv := transcript.Serialize(msgs, transcript.SerializeOptions{})
	raw, err := pipe.Classifier.Classify(ctx, rolemanager.BuildCompactionPayload(conv))
	if err != nil {
		return nil, false
	}
	summary, err := rolemanager.ValidateSummary(raw)
	if err != nil {
		return nil, false
	}
	dec, err := pipe.Admit(ctx, summary, s.posture)
	if err != nil || dec.Action != rolemanager.ActionProceed {
		return nil, false
	}
	return []run.Turn{
		{Role: "user", Content: rolemanager.SummaryPrefix + dec.Content + rolemanager.SummarySuffix},
		{Role: "assistant", Content: rolemanager.SummaryAck},
	}, true
}
