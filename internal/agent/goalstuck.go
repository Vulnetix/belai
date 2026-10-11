package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/vulnetix/belai/internal/clarify"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
)

// A goal loop has no pass ceiling. The one thing that breaks it, short of the
// goal being met, a cancellation or a safety stop, is being stuck: the same
// work goalStuckPasses times in a row with no file changed (or as many passes
// in a row that could run no tool, or an evaluator that cannot be read). The
// harness then asks the model to ask the user how to proceed. An answer is
// steering: the stall counters reset and the loop carries on under it. No
// answer (declined, nobody to ask) ends the goal with its report.
//
// The model writes the question, because it knows where it is stuck. When it
// does not ask (it carried on, or asked something already asked), the harness
// asks a fixed question of its own, so a stuck goal never loops on.

// Outcomes of the AskUserQuestion calls a stuck pass makes (Session.askOutcome).
const (
	askOutcomeNone int32 = iota
	askOutcomeAnswered
	askOutcomeDeclined
)

// stuckAskDirective asks the model to put the stall to the user.
const stuckAskDirective = "The goal is stuck: %s. Stop the work for now and ask the user how to proceed: one AskUserQuestion call that says in a sentence where you are stuck and offers the concrete ways forward you see. When the user answers, follow their direction in this same response."

// stuckSteeredDirective follows steering the user gave a stuck goal.
const stuckSteeredDirective = "The user has answered how to proceed (above). Their direction outranks your earlier approach: carry it out now, update the Todo list to match, and do not repeat the work that stalled."

// stuckStopLabel is the harness question's option that ends the goal.
const stuckStopLabel = "Stop the goal here"

// stuckQuestionnaire is the harness's own question when the model did not ask.
func stuckQuestionnaire() clarify.Questionnaire {
	return clarify.Questionnaire{Groups: []clarify.Group{{
		Context: "The goal has stopped making progress: the same work repeated without changing anything. How should it proceed?",
		Options: []clarify.Option{
			{Label: "Try a different approach", Description: "Add a note with the direction to take."},
			{Label: stuckStopLabel, Description: "End the goal and report what was done."},
		},
	}}}
}

// steeredBy reports whether answers to the harness question are steering:
// anything but a bare "stop".
func steeredBy(q clarify.Questionnaire, a clarify.Answers) bool {
	if a.SkippedAll() {
		return false
	}
	for _, it := range a.Items {
		if it.Note != "" {
			return true
		}
		for _, c := range it.Chosen {
			if c >= 0 && it.GroupIndex >= 0 && it.GroupIndex < len(q.Groups) && c < len(q.Groups[it.GroupIndex].Options) &&
				q.Groups[it.GroupIndex].Options[c].Label != stuckStopLabel {
				return true
			}
		}
	}
	return false
}

// goalStuck asks the user how a stuck goal should proceed. It returns the
// turns with the exchange appended and whether the user steered.
func (s *Session) goalStuck(ctx context.Context, pipe *rolemanager.Pipeline, system string, turns []run.Turn, streaming bool, emit func(Event), l *passLedger, why string) ([]run.Turn, bool, error) {
	if !s.canAskUser() {
		emit(Event{Kind: EventWarningKind, Warning: fmt.Sprintf("goal stuck: %s; nobody can be asked how to proceed, so the goal ends here", why)})
		return turns, false, nil
	}
	emit(Event{Kind: EventWarningKind, Warning: fmt.Sprintf("goal stuck: %s; asking the user how to proceed", why)})

	s.askOutcome.Store(askOutcomeNone)
	turns = append(turns, directiveTurns(fmt.Sprintf(stuckAskDirective, why))...)
	l.passes++
	emit(Event{Kind: EventPassKind, Pass: l.passes})
	out, turns, err := s.pass(ctx, pipe, system, turns, streaming, emit, modes.ModeGoal)
	if err != nil {
		return turns, false, err
	}
	l.noteWrites(out)
	l.advanceTodos(out.text)
	if out.reply != "" && !out.exhausted {
		turns = withReply(turns, out.reply)
	}

	switch s.askOutcome.Load() {
	case askOutcomeAnswered:
		s.askMu.Lock()
		answer := s.lastAnswer
		s.askMu.Unlock()
		l.steer(answer)
		return append(turns, directiveTurns(stuckSteeredDirective)...), true, nil
	case askOutcomeDeclined:
		return turns, false, nil
	}

	// The model did not ask: the harness asks its own question.
	q := stuckQuestionnaire()
	ans, ok := s.askUser(ctx, q, emit)
	if !ok || !steeredBy(q, ans) {
		return turns, false, nil
	}
	turn, ok := s.admitAnswers(ctx, pipe, ans.Render(q), emit)
	if !ok {
		return turns, false, nil
	}
	turns = append(turns, turn)
	l.steer(turn.Content)
	return append(turns, directiveTurns(stuckSteeredDirective)...), true, nil
}

// steer adds the user's answer to a stuck goal to the goal text, as user
// direction: the evaluator judges the goal as the user now wants it, the same
// way a continuation's extra direction rides on the objective. Without it the
// evaluator held the model to the approach the user had just overruled.
func (l *passLedger) steer(answer string) {
	if a := strings.TrimSpace(answer); a != "" {
		l.goalText += "\n\nDirection from the user after the goal stalled (it outranks the objective above where they conflict):\n" + a
	}
}

// resetStall clears every stall counter after the user steered a stuck goal:
// the loop starts over under the new direction.
func (l *passLedger) resetStall() {
	l.repeatPasses = 0
	l.lastPassPrint = ""
	l.partialStreak = 0
	l.passesSinceWrite = 0
	l.unproductivePasses = 0
	l.malformedStreak = 0
	l.evalErrorStreak = 0
}

// stuckError ends the loop when the stuck pass itself failed: a cancellation
// reads as one, never as an agent error.
func (s *Session) stuckError(ctx context.Context, l passLedger, out passOutcome, err error) (run.Result, error) {
	if ctx.Err() != nil {
		return run.Result{Reply: out.lastText, GoalSentinel: rolemanager.GoalPartial, Passes: l.passes}, ErrPassLoopCancelled
	}
	s.turnStop = run.StopStalled
	return run.Result{Passes: l.passes}, maybeCompact(err)
}
