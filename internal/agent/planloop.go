package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/plans"
	"github.com/vulnetix/belai/internal/prompt"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/todos"
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
const defaultPlanContinuations = 3

// Harness-injected plan-mode continuation instructions. The bodies are sealed
// into <directive> blocks at egress exactly like the goal loop's; only the
// wording is plan-mode, so a plan-mode turn never tells the model it is
// pursuing a goal.
const (
	planPlanningDirective = "The plan has not started yet. Find the files the request touches (Glob, Grep), read only those, decide every open point, then call ExitPlanMode with the whole plan (# title, ## Summary, ## Key Changes with Files and Verify per step, ## Test Plan, ## Assumptions). Reading is bounded; the plan is the deliverable."
	// planBudgetNote is prefixed to a plan continuation directive when the
	// pass that just ended spent its whole tool budget. The counter has reset
	// for the next pass, so the model must treat the boundary as a
	// continuation, never a stop.
	planBudgetNote = "Your tool-call budget for that pass was reached and has been reset for this pass. "
)

// planLedger is the loop-local decision state of one plan pass loop. Like the
// goal loop's passLedger, pass index and sentinel history live here — never in
// turns and never re-parsed out of transcript text. Plan mode keeps its own
// ledger so no goal-mode field (or a leftover memorised goal) can leak into
// the plan path.
type planLedger struct {
	// repaired is set once a repair continuation has been spent: a pass that
	// failed on its own calls gets one more pass to correct them.
	repaired  bool
	passes    int
	maxPasses int    // bounded ceiling, for the escalating finish-or-check-in wording
	context   string // exploration findings shown to the plan evaluator
	lastText  string // latest non-empty assistant text of the last finished pass
	bestPlan  string // latest plan-shaped assistant text; the artifact fallback

	// plan todo list, shared with the TUI panel.
	list        todos.List
	hasList     bool
	todoChanged bool // true when the pass that just ended advanced the list

	// malformedStreak counts consecutive malformed evaluator replies; a clean
	// reply resets it.
	malformedStreak int

	// writeNow makes the next pass the final, write-only pass: a pass spent
	// its whole read budget without drafting any plan, so another research
	// pass would only read more (and re-read what context clearing dropped).
	writeNow bool

	// overflowRetried: a ClassOverflow escaping pass is caught once (compact,
	// re-run the pass); a second overflow is terminal.
	overflowRetried bool

	// read are the file paths the model has already read this turn, in order,
	// deduplicated and bounded. They come from the model's own tool-call
	// arguments, never from tool results, and exist so the continuation
	// directive can say what is already known: sessions showed every pass
	// reopening with "let me ground myself" and re-reading the same files.
	read []string
	// reason is the evaluator's one-line account of what the plan still
	// lacks. It is model output and rides the next directive turn as plain
	// text, never inside the sealed directive.
	reason string
}

// maxPlanReadPaths bounds the already-read list the directive names.
const maxPlanReadPaths = 30

// readTools are the tools whose path argument means "the model has these
// bytes in context".
var readTools = map[string]bool{"Read": true, "Cat": true, "Head": true, "Tail": true, "RepoRead": true}

// noteReads records the paths a pass's read calls named.
func (l *planLedger) noteReads(passTurns []run.Turn) {
	for _, t := range passTurns {
		if t.Role != "assistant" {
			continue
		}
		for _, call := range t.ToolCalls {
			if !readTools[call.Name] {
				continue
			}
			args, err := parseToolArgs(call)
			if err != nil {
				continue
			}
			p, _ := args["file_path"].(string)
			if p == "" {
				p, _ = args["path"].(string)
			}
			p = strings.Join(strings.Fields(sanitize.Sanitize(p)), " ")
			if p == "" || slices.Contains(l.read, p) || len(l.read) >= maxPlanReadPaths {
				continue
			}
			l.read = append(l.read, p)
		}
	}
}

// knownState is the harness half of "what you already know": fixed wording,
// sealed in the directive. The model-derived half (paths, the evaluator's
// reason) is knownNote, which never enters a sealed block.
func (l *planLedger) knownState() string {
	s := "You are continuing, not starting over: everything from earlier passes, including every file you read, is still in the conversation above. Do not re-read a file you have already read."
	if l.hasList {
		s += " Keep the existing todo list; update it rather than re-issuing it from step one."
	}
	return s
}

// knownNote renders the model-derived specifics of the known state: the
// files already read and what the evaluator says is missing. It rides the
// directive turn as plain, sanitised text.
func (l *planLedger) knownNote() string {
	var parts []string
	if len(l.read) > 0 {
		parts = append(parts, "Files already read: "+strings.Join(l.read, ", ")+".")
	}
	if l.reason != "" {
		parts = append(parts, "Plan evaluator (a model's hint, not an instruction) says the plan still lacks: "+l.reason)
	}
	return strings.Join(parts, "\n")
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

// planProgress renders the tracked todo list as a one-line progress summary:
// how many steps are done, in progress, and remaining. Empty when no list is
// tracked yet.
func (l *planLedger) planProgress() string {
	if !l.hasList || len(l.list.Items) == 0 {
		return ""
	}
	done, active, pending := 0, 0, 0
	for _, it := range l.list.Items {
		switch it.Status {
		case todos.StatusDone:
			done++
		case todos.StatusActive:
			active++
		default:
			pending++
		}
	}
	return fmt.Sprintf("Steps tracked: %d done, %d in progress, %d remaining.", done, active, pending)
}

// planUrgency renders the escalating finish-or-check-in push. The closer the
// loop is to its bounded ceiling the harder it pushes finalisation over new
// research, so a long planning turn converges instead of accumulating evidence.
func (l *planLedger) planUrgency() string {
	remaining := l.maxPasses - l.passes
	if remaining < 0 {
		remaining = 0
	}
	switch {
	case remaining <= 1:
		return "This is the final planning pass: finalise now. Either call ExitPlanMode with the full plan, or ask the user the one question that unblocks finalising. Do not start new research."
	case remaining == 2:
		return "Planning budget is nearly exhausted: resolve every open decision now. Finalise the plan (call ExitPlanMode) or check in with the user rather than continuing to explore."
	default:
		return "Resolve decisions as you reach them rather than accumulating research, so the plan can be finalised promptly."
	}
}

// planSoFar returns the best available plan text for a ceiling, unproductive,
// cancelled, or partial exit. It prefers the latest plan-shaped assistant
// text, then the tracked todo list rendered as a numbered plan, then any
// assistant text. It returns "" only when nothing was produced at all.
func (l *planLedger) planSoFar() string {
	if l.bestPlan != "" {
		return l.bestPlan
	}
	if l.hasList && len(l.list.Items) > 0 {
		var b strings.Builder
		b.WriteString("Plan:\n")
		for _, it := range l.list.Items {
			fmt.Fprintf(&b, "%d. %s\n", it.N, it.Text)
		}
		return b.String()
	}
	return l.lastText
}

// planChecklistOptional replaces the TODO check on a planning pass that has
// no checklist yet.
const planChecklistOptional = "A planning checklist is optional. If you keep one, send update_plan in the same response as your reads, never as a response of its own. When you have what the plan needs, call ExitPlanMode with it."

// planStartDirective builds the PLAN_NOT_STARTED continuation instruction.
// exhausted reports whether the pass spent its whole tool budget.
func (l *planLedger) planStartDirective(exhausted bool) string {
	body := planPlanningDirective
	if l.passes > 0 {
		body = l.knownState() + " " + planPlanFromWhatYouHave
	}
	if exhausted {
		return planBudgetNote + body
	}
	return body
}

// planPlanFromWhatYouHave replaces the from-scratch planning directive after
// the first pass: the research is already in context, so the next step is to
// write the plan from it.
const planPlanFromWhatYouHave = "Write the plan now from what you have already gathered and call ExitPlanMode with it: # title, ## Summary, ## Key Changes (numbered steps naming the files and the exact changes, each with Files and Verify), ## Test Plan, ## Assumptions. Read further only for one specific fact the plan cannot be written without; anything else unverified goes under Assumptions."

// planFinalDirective leads the plan loop's last pass, whose tool surface is
// update_plan and ExitPlanMode only.
const planFinalDirective = "This is the final planning pass. Only ExitPlanMode and AskUserQuestion are available; there is no more reading. Write the complete plan from what is already in the conversation (# title, ## Summary, ## Key Changes with Files and Verify per step, ## Test Plan, ## Assumptions) and call ExitPlanMode with it now, in this response. If a decision is genuinely the user's and nothing in the conversation or the code settles it, ask it with AskUserQuestion instead (never a question already asked or already answered); otherwise choose a default and record it under Assumptions rather than researching it."

// planPartialDirective builds the continuation instruction for a PLAN_PARTIAL
// verdict: the current plan list state, a progress summary, and an escalating
// finish-or-check-in push. exhausted reports whether the pass spent its whole
// tool budget, in which case the model is told the counter has reset.
func (l *planLedger) planPartialDirective(exhausted bool) string {
	var b strings.Builder
	if exhausted {
		b.WriteString(planBudgetNote)
	}
	b.WriteString(l.knownState() + " ")
	b.WriteString("The plan is partially complete.")
	if p := l.planProgress(); p != "" {
		b.WriteString(" " + p)
	}
	b.WriteString(" " + l.planUrgency())
	if l.hasList {
		b.WriteString(" Fold the concrete tool calls you have made (files read, commands run) into the plan's implementation stages, then continue. Mark steps complete with [DONE:n] in your reply as you finish them.")
	} else {
		b.WriteString(" Write a planning todo list under a 'Plan:' header (numbered steps), and fill in each step from what you have already read. Mark each step complete with [DONE:n] in your reply as you finish it.")
	}
	return b.String()
}

// partialPlanResult builds the result returned on terminal error paths. The
// plan-file contract is that every plan-mode turn writes a file — partial,
// ceiling, cancelled, unproductive, or failed — so the reply (the latest plan
// text the model produced) and a PLAN_PARTIAL sentinel ride the result when
// the text is actually a plan. recordPlan writes the file from those fields;
// the error itself still propagates as terminal. A terminal turn whose reply
// was never plan-shaped (e.g. the model only answered "done") records nothing,
// so an empty or invalid plan can never be written to disk as an artifact.
func (l *planLedger) partialPlanResult(latest string) run.Result {
	reply := latest
	if !hasPlan(reply) {
		reply = l.planSoFar()
	}
	if !hasPlan(reply) {
		return run.Result{Passes: l.passes, PlanSentinel: rolemanager.PlanPartial}
	}
	return run.Result{Reply: reply, Passes: l.passes, PlanSentinel: rolemanager.PlanPartial}
}

// hasPlan reports whether text carries a plan worth recording: a numbered plan
// under a "Plan:" header, or a structured markdown plan with numbered steps.
// It mirrors the two parsers recordPlan canonicalises with.
func hasPlan(text string) bool {
	if _, err := plans.ExtractSteps(text); err == nil {
		return true
	}
	_, err := plans.ParseDoc(text)
	return err == nil
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

	l := planLedger{context: planContext, maxPasses: maxPasses}
	defer func() { s.planFinalPass = false }()

	for {
		// Cancellation returns the partial result cleanly, never a raw
		// context.Canceled.
		if err := ctx.Err(); err != nil {
			return run.Result{Reply: l.planSoFar(), Passes: l.passes, PlanSentinel: rolemanager.PlanPartial}, ErrPlanLoopCancelled
		}

		if l.passes >= maxPasses {
			// The bounded ceiling is a turn boundary: return the plan so far
			// with a system note, never an error.
			emit(Event{Kind: EventWarningKind, Warning: fmt.Sprintf("plan pass loop stopped: max passes (%d) reached; returning the plan so far", maxPasses)})
			return run.Result{Reply: l.planSoFar(), Passes: l.passes, PlanSentinel: rolemanager.PlanPartial}, nil
		}

		l.passes++
		emit(Event{Kind: EventPassKind, Pass: l.passes})

		// Proactive compaction at the pass boundary, mirroring the goal loop.
		if compacted, ok := s.compactBoundary(ctx, pipe, turns); ok {
			turns = compacted
		}

		start := len(turns)
		// The planning contract rides a hidden harness directive, sealed at
		// egress and never rendered in the transcript: full on pass 1 and
		// every fifth pass, a one-line reminder in between.
		// Before a planning checklist exists, recording one is optional: the
		// plan is the deliverable, and the mandatory "call update_plan" check
		// made the model spend a whole round writing a checklist before it
		// read anything. Once a list is tracked, the check keeps it current.
		if l.hasList && len(l.list.Items) > 0 {
			turns = append(turns, withPlanCheck(prompt.PlanDirective(l.passes), l.list, l.hasList)...)
		} else {
			turns = append(turns, directiveTurns(prompt.PlanDirective(l.passes)+"\n\n"+planChecklistOptional)...)
		}
		// The last allowed pass offers only ExitPlanMode and AskUserQuestion, so
		// the loop ends on a plan, never on one more round of reading.
		final := l.passes >= maxPasses || l.writeNow
		s.planFinalPass = final
		if final {
			turns = append(turns, withPlanCheck(l.knownState()+" "+planFinalDirective, l.list, l.hasList, l.knownNote())...)
		}
		out, turns, err := s.pass(ctx, pipe, system, turns, streaming, emit, modes.ModePlan)
		s.planFinalPass = false
		// A plan the lint sent back is still a plan. If no corrected one arrives,
		// it is what the turn records, not nothing.
		if l.bestPlan == "" && s.lintRejected != "" && hasPlan(s.lintRejected) {
			l.bestPlan = s.lintRejected
		}
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				reply := out.lastText
				if reply == "" {
					reply = l.planSoFar()
				}
				return run.Result{Reply: reply, Usage: out.usage, PlanSentinel: rolemanager.PlanPartial, Passes: l.passes}, ErrPlanLoopCancelled
			}
			if !l.overflowRetried && isOverflow(err) {
				l.overflowRetried = true
				if compacted, ok := s.recoverOverflow(ctx, pipe, turns); ok {
					turns = compacted
					out, turns, err = s.pass(ctx, pipe, system, turns, streaming, emit, modes.ModePlan)
				}
			}
			if err != nil {
				return l.partialPlanResult(out.lastText), maybeCompact(err)
			}
		}

		l.noteReads(turns[start:])
		l.advancePlan(out.text)
		if out.lastText != "" {
			l.lastText = out.lastText
		}
		if hasPlan(out.lastText) {
			l.bestPlan = out.lastText
		}
		if out.updatePlan != nil {
			if !l.hasList {
				l.list = todos.New(l.context, nil)
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

		// The planner asked the user. The loop ends here; the session shows the
		// questions and runs the answers as a new agent-mode turn.
		if out.askUser != nil {
			return run.Result{Reply: out.lastText, Usage: out.usage, Passes: l.passes, Clarify: out.askUser}, nil
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
			reply := out.lastText
			if reply == "" {
				reply = out.planText
			}
			return run.Result{
				Reply:        reply,
				Usage:        out.usage,
				PlanSentinel: rolemanager.PlanComplete,
				PlanText:     out.planText,
				Passes:       l.passes,
			}, nil
		}

		// A pass that ends with a finished plan document as its reply, no
		// tool call after it, is the plan: the model wrote the deliverable
		// where ExitPlanMode's argument should have been (session v2-T1).
		// The document is the evidence, so no evaluator call is spent on it,
		// and only the document is recorded, without the narration before it.
		// On the final pass an exhausted reply counts too.
		candidate := ""
		if !out.exhausted {
			candidate = out.reply
		} else if final {
			candidate = out.lastText
		}
		if doc, ok := plans.ExtractDoc(candidate); ok {
			if l.hasList {
				l.list.MarkAllDone()
				list := l.list
				emit(Event{Kind: EventTodosKind, Todos: &list})
			}
			return run.Result{
				Reply:        out.lastText,
				Usage:        out.usage,
				PlanSentinel: rolemanager.PlanComplete,
				PlanText:     doc,
				Passes:       l.passes,
			}, nil
		}

		// The final pass had nothing to do but write the plan. If it wrote
		// one without calling ExitPlanMode, that reply is the plan; there is
		// no further pass for an evaluator verdict to buy.
		if final {
			reply := l.planSoFar()
			if reply == "" {
				reply = out.lastText
			}
			emit(Event{Kind: EventWarningKind, Warning: fmt.Sprintf("plan pass loop reached its last pass (%d) without ExitPlanMode; returning the plan written so far", maxPasses)})
			return run.Result{Reply: reply, Usage: out.usage, Passes: l.passes, PlanSentinel: rolemanager.PlanPartial}, nil
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
				// A pass that failed only on errors in its own calls is
				// repairable: one more pass with the repair request, then the
				// plan so far.
				if out.repairFailures > 0 && !l.repaired {
					l.repaired = true
					emit(Event{Kind: EventWarningKind, Warning: fmt.Sprintf("planning pass %d ended on failing tool calls; asking for corrected calls", l.passes)})
					turns = append(turns, withPlanCheck(repairContinuationDirective, l.list, l.hasList)...)
					continue
				}
				emit(Event{Kind: EventWarningKind, Warning: fmt.Sprintf("plan pass loop stopped: pass %d executed no tools; returning the plan so far", l.passes)})
				return run.Result{Reply: l.planSoFar(), Passes: l.passes, PlanSentinel: rolemanager.PlanPartial}, nil
			}
			// A whole read budget spent with no plan drafted: the next pass
			// writes it. Left to the evaluator, "partial, no plan yet" bought
			// another pass of reading — twice in session b3a026a4, 23
			// minutes each. The evaluator still runs for its reason, which
			// the final directive carries.
			if l.bestPlan == "" && !l.writeNow {
				l.writeNow = true
				emit(Event{Kind: EventWarningKind, Warning: fmt.Sprintf("planning pass %d spent its read budget with no plan drafted; the next pass writes it", l.passes)})
				// The next pass is write-only whatever the evaluator says:
				// its reason still rides that pass, but a PLAN_COMPLETE
				// verdict cannot cancel it (see the guard below; session
				// d9292a3d returned a pass's last sentence as the plan).
			}
		}

		verdict, evalErr := rolemanager.EvaluatePlanVerdict(ctx, pipe.Classifier, rolemanager.PlanEvalInput{
			Context:  l.context,
			Todos:    l.list.Render(),
			Evidence: sanitize.Sanitize(evidence),
		})
		sentinel := verdict.Sentinel
		l.reason = verdict.Reason
		if evalErr != nil {
			if !errors.Is(evalErr, rolemanager.ErrMalformedPlanEval) {
				// Transport failure: terminal. The verdict is unknown, and an
				// unknown verdict must not grant compute — but the plan
				// gathered so far is still written to disk before the error
				// surfaces, so the turn keeps its plan-file artifact.
				return l.partialPlanResult(l.lastText), evalErr
			}
			// Malformed output fails closed to PLAN_PARTIAL (one garbled reply
			// is noise); two in a row is a broken evaluator.
			l.malformedStreak++
			emit(Event{Kind: EventPlanEvalKind, Pass: l.passes, PlanSentinel: sentinel, Malformed: true})
			if l.malformedStreak >= 2 {
				return l.partialPlanResult(l.lastText),
					fmt.Errorf("plan pass loop stopped: %d consecutive malformed evaluator replies", l.malformedStreak)
			}
		} else {
			l.malformedStreak = 0
			emit(Event{Kind: EventPlanEvalKind, Pass: l.passes, PlanSentinel: sentinel, EvalReason: verdict.Reason})
		}

		// A verdict of complete is only as good as the plan it refers to.
		// The evaluator sees a checklist and a digest, so a pass whose every
		// research step is done but whose text holds no plan can read as
		// complete; accepting that records a sentence as the plan. The
		// harness holds the one fact that decides it — whether any plan
		// text exists — so a plan-less "complete" becomes one write-only
		// pass instead.
		if sentinel == rolemanager.PlanComplete && !hasPlan(out.lastText) && l.bestPlan == "" {
			emit(Event{Kind: EventWarningKind, Warning: fmt.Sprintf("plan evaluator reported complete after pass %d but no plan was written; the next pass writes it", l.passes)})
			l.writeNow = true
			turns = append(turns, directiveTurnsWithNote(l.planStartDirective(out.exhausted), l.knownNote())...)
			continue
		}

		switch sentinel {
		case rolemanager.PlanComplete:
			// Accepted: mark the plan list complete and return the reply.
			if l.hasList {
				l.list.MarkAllDone()
				list := l.list
				emit(Event{Kind: EventTodosKind, Todos: &list})
			}
			reply := out.lastText
			if reply == "" {
				reply = l.planSoFar()
			}
			return run.Result{
				Reply:        reply,
				Usage:        out.usage,
				PlanSentinel: sentinel,
				Passes:       l.passes,
			}, nil

		case rolemanager.PlanNotStarted:
			// The explore wave already ran before the loop, so there is no
			// forced survey here: the exploration context is already in the
			// conversation and in l.context. Inject the planning directive.
			turns = append(turns, directiveTurnsWithNote(l.planStartDirective(out.exhausted), l.knownNote())...)

		case rolemanager.PlanPartial:
			turns = append(turns, directiveTurnsWithNote(l.planPartialDirective(out.exhausted), l.knownNote())...)

		default:
			// Unreachable: ParsePlanSentinel accepts only the three sentinels,
			// and malformed input resolves to PLAN_PARTIAL.
			return l.partialPlanResult(l.lastText), fmt.Errorf("plan pass loop: unknown verdict %q", sentinel)
		}
	}
}
