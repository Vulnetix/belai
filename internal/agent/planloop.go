package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/vulnetix/signet/internal/plans"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/sanitize"
	"github.com/vulnetix/signet/internal/todos"
)

// ErrPlanLoopCancelled reports a deliberate cancellation of the plan pass
// loop. It is distinct from a raw context.Canceled so the UI can show a clean
// "cancelled" notice instead of an agent error, and distinct from
// ErrPassLoopCancelled so a plan-mode cancellation never reads as a goal-mode
// one.
var ErrPlanLoopCancelled = errors.New("plan pass loop cancelled")

// defaultPlanContinuations bounds the plan pass loop when resilience.max_passes
// is unset (0). Plan mode is bounded by design: it produces a plan, then the
// user executes it, so it never inherits goal mode's unbounded-by-default
// behaviour. Reaching the ceiling returns the plan so far with a system note —
// a turn boundary, not an error.
const defaultPlanContinuations = 5

// Harness-injected plan-mode continuation instructions. The bodies are sealed
// into <directive> blocks at egress exactly like the goal loop's; only the
// wording is plan-mode, so a plan-mode turn never tells the model it is
// pursuing a goal.
const (
	planPlanningDirective = "The plan has not started yet. Write a planning todo list under a 'Plan:' header (numbered steps), then begin researching the first step. Mark each step complete with [DONE:n] in your reply as you finish it."
	// planContinuationDirective is injected when a natural-exit pass is not
	// accepted as complete: the model stopped calling tools but the plan is
	// not done, so it must keep going.
	planContinuationDirective = "The plan is not complete yet. Report what remains to research and continue from the current plan todo list. Mark steps complete with [DONE:n] in your reply as you finish them."
)

// planLedger is the loop-local decision state of one plan pass loop. Like the
// goal loop's passLedger, pass index and sentinel history live here — never in
// turns and never re-parsed out of transcript text. Plan mode keeps its own
// ledger so no goal-mode field (or a leftover memorised goal) can leak into
// the plan path.
type planLedger struct {
	passes   int
	context  string // exploration findings shown to the plan evaluator
	lastText string // latest assistant text of the last finished pass

	// plan todo list, shared with the TUI panel.
	list        todos.List
	hasList     bool
	todoChanged bool // true when the pass that just ended advanced the list

	// malformedStreak counts consecutive malformed evaluator replies; a clean
	// reply resets it.
	malformedStreak int

	// overflowRetried: a ClassOverflow escaping pass is caught once (compact,
	// re-run the pass); a second overflow is terminal.
	overflowRetried bool
}

// advancePlan maintains the plan todo list from model-authored assistant text
// only. out.text is the pass's accumulated assistant text — never tool
// results, never turns. A "[DONE:n]" marker in a repository file must not mark
// a plan step complete: completion is an input to whether the loop stops.
func (l *planLedger) advancePlan(passAssistantText string) {
	l.todoChanged = false
	state := l.turnState()
	if l.hasList {
		l.list.ApplyMarkers(passAssistantText)
	} else if steps, err := plans.ExtractSteps(passAssistantText); err == nil && len(steps) > 0 {
		l.list = todos.New(l.context, steps)
		l.hasList = true
	}
	if l.turnState() != state {
		l.todoChanged = true
	}
}

// turnState renders the plan todo list as a state key for progress detection.
func (l *planLedger) turnState() string {
	if !l.hasList {
		return ""
	}
	var b strings.Builder
	for _, it := range l.list.Items {
		fmt.Fprintf(&b, "%d:%s ", it.N, it.Status)
	}
	return b.String()
}

// planPartialDirective builds the continuation instruction for a PLAN_PARTIAL
// verdict: the current plan list state when one exists, otherwise the
// instruction to start tracking one.
func (l *planLedger) planPartialDirective() string {
	if l.hasList {
		return "The plan is partially complete. Continue from the current plan todo list state:\n\n" +
			l.list.Render() +
			"\n\nMark steps complete with [DONE:n] in your reply as you finish them."
	}
	return "The plan is partially complete but no plan todo list is tracked yet. Write a planning todo list under a 'Plan:' header (numbered steps), then continue researching. Mark each step complete with [DONE:n] in your reply as you finish it."
}

// planPassLoop drives the plan-mode pass loop. It is the plan-mode analogue of
// the goal pass loop's boundary contact: when a pass exhausts its iteration
// budget (or ends naturally), the harness contacts a plan evaluator through the
// Role Manager classifier, showing it the exploration context the explore
// agents gathered for the session — never a goal definition — plus the plan
// todo list and the pass evidence. The verdicts are PLAN_* sentinels, not
// GOAL_* ones.
//
// Unlike the goal loop, the plan loop is bounded and has no verification gate:
// plan mode is read-only and hands a plan to the user for review, so the
// goal loop's disk-re-check gate (which exists because goal mode mutates
// files) has nothing to verify. resilience.max_passes (0 falls back to
// defaultPlanContinuations) is a ceiling that returns the plan so far, never
// an error.
func (s *Session) planPassLoop(ctx context.Context, pipe *rolemanager.Pipeline, system string, turns []run.Turn, planContext string, streaming bool, emit func(Event)) (run.Result, error) {
	maxPasses := s.settings.Resilience.MaxPassesOr()
	if maxPasses <= 0 {
		maxPasses = defaultPlanContinuations
	}

	l := planLedger{context: planContext}

	for {
		// Cancellation returns the partial result cleanly, never a raw
		// context.Canceled.
		if err := ctx.Err(); err != nil {
			return run.Result{Passes: l.passes}, ErrPlanLoopCancelled
		}

		if l.passes >= maxPasses {
			// The bounded ceiling is a turn boundary: return the plan so far
			// with a system note, never an error.
			emit(Event{Kind: EventWarningKind, Warning: fmt.Sprintf("plan pass loop stopped: max passes (%d) reached; returning the plan so far", maxPasses)})
			return run.Result{Reply: l.lastText, Passes: l.passes, PlanSentinel: rolemanager.PlanPartial}, nil
		}

		l.passes++
		emit(Event{Kind: EventPassKind, Pass: l.passes})

		// Proactive compaction at the pass boundary, mirroring the goal loop.
		if compacted, ok := s.compactBoundary(ctx, pipe, turns); ok {
			turns = compacted
		}

		start := len(turns)
		out, turns, err := s.pass(ctx, pipe, system, turns, streaming, emit)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return run.Result{Reply: out.lastText, Usage: out.usage, PlanSentinel: rolemanager.PlanPartial, Passes: l.passes}, ErrPlanLoopCancelled
			}
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

		l.advancePlan(out.text)
		l.lastText = out.lastText
		if l.todoChanged && l.hasList {
			list := l.list
			emit(Event{Kind: EventTodosKind, Todos: &list})
		}

		// Model-declared completion: a direct ExitPlanMode call overrides the
		// evaluator, saving a model round-trip and letting the planning model
		// finish its own turn.
		if out.planExit {
			if l.hasList {
				l.list.MarkAllDone()
				list := l.list
				emit(Event{Kind: EventTodosKind, Todos: &list})
			}
			return run.Result{
				Reply:        out.lastText,
				Usage:        out.usage,
				PlanSentinel: rolemanager.PlanComplete,
				Passes:       l.passes,
			}, nil
		}

		// Natural exit: a no-tool-call reply is a claim of completion, not
		// proof, so it is re-checked against the reply text itself. An
		// exhausted pass is evaluated against the pass's turn digest.
		var evidence string
		if !out.exhausted {
			evidence = out.reply
		} else {
			evidence = evidenceDigest(turns[start:])
		}

		// Natural-exit fast path: if the assistant produced a plan with steps
		// and every tracked step is already marked done, treat the plan as
		// complete without consulting the evaluator.
		if !out.exhausted {
			if steps, err := plans.ExtractSteps(out.reply); err == nil && len(steps) > 0 && l.hasList && l.list.Complete() {
				return run.Result{
					Reply:        out.lastText,
					Usage:        out.usage,
					PlanSentinel: rolemanager.PlanComplete,
					Passes:       l.passes,
				}, nil
			}
		}

		// Steering outranks the evaluator: explicit user intent beats a
		// classifier, and skipping the call saves a model round-trip.
		if out.exhausted {
			if steer := s.drainSteer(ctx, pipe, emit); len(steer) > 0 {
				turns = append(turns, steer...)
				continue
			}
			// A zero-productive pass burns its budget without doing any work;
			// it produces no evidence and must not buy another pass. In plan
			// mode this is a turn boundary: return the plan gathered so far so
			// the harness can write it to disk for review.
			if out.productive == 0 {
				emit(Event{Kind: EventWarningKind, Warning: fmt.Sprintf("plan pass loop stopped: pass %d executed no tools; returning the plan so far", l.passes)})
				return run.Result{Reply: l.lastText, Passes: l.passes, PlanSentinel: rolemanager.PlanPartial}, nil
			}
		}

		sentinel, evalErr := rolemanager.EvaluatePlan(ctx, pipe.Classifier, rolemanager.PlanEvalInput{
			Context:  l.context,
			Todos:    l.list.Render(),
			Evidence: sanitize.Sanitize(evidence),
		})
		if evalErr != nil {
			if !errors.Is(evalErr, rolemanager.ErrMalformedPlanEval) {
				// Transport failure: terminal. The verdict is unknown, and an
				// unknown verdict must not grant compute.
				return run.Result{Passes: l.passes}, evalErr
			}
			// Malformed output fails closed to PLAN_PARTIAL (one garbled reply
			// is noise); two in a row is a broken evaluator.
			l.malformedStreak++
			emit(Event{Kind: EventPlanEvalKind, Pass: l.passes, PlanSentinel: sentinel})
			if l.malformedStreak >= 2 {
				return run.Result{Passes: l.passes, PlanSentinel: sentinel},
					fmt.Errorf("plan pass loop stopped: %d consecutive malformed evaluator replies", l.malformedStreak)
			}
		} else {
			l.malformedStreak = 0
			emit(Event{Kind: EventPlanEvalKind, Pass: l.passes, PlanSentinel: sentinel})
		}

		switch sentinel {
		case rolemanager.PlanComplete:
			// Accepted: mark the plan list complete and return the reply.
			if l.hasList {
				l.list.MarkAllDone()
				list := l.list
				emit(Event{Kind: EventTodosKind, Todos: &list})
			}
			return run.Result{
				Reply:        out.lastText,
				Usage:        out.usage,
				PlanSentinel: sentinel,
				Passes:       l.passes,
			}, nil

		case rolemanager.PlanNotStarted:
			// The explore wave already ran before the loop, so there is no
			// forced survey here: the exploration context is already in the
			// conversation and in l.context. Inject the planning directive.
			turns = append(turns, directiveTurns(planPlanningDirective)...)

		case rolemanager.PlanPartial:
			turns = append(turns, directiveTurns(l.planPartialDirective())...)

		default:
			// Unreachable: ParsePlanSentinel accepts only the three sentinels,
			// and malformed input resolves to PLAN_PARTIAL.
			return run.Result{Passes: l.passes}, fmt.Errorf("plan pass loop: unknown verdict %q", sentinel)
		}
	}
}
