package agent

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/sanitize"
)

// The goal contract is an aid, not a prerequisite, so the goal never waits
// for it. Real sessions showed a draft holding the first pass back for
// minutes behind a slow model; a 45s grace still cost every goal turn up to
// 45s of dead time before the main model was asked anything, and a
// reasoning-model draft routinely used all of it and was then thrown away.
//
// The draft runs alongside exploration. When the loop starts it takes the
// draft if it is already done, and otherwise starts on the raw prompt at once
// and keeps polling: a draft that lands later is adopted at the next pass
// boundary as a sealed directive (see adoptLateGoalDraft), so the sealed
// system block — and the provider's prompt cache — is not disturbed.
//
// goalDraftCeiling bounds the draft itself; the turn's end cancels it too.
const goalDraftCeiling = 2 * time.Minute

// pendingDraft is a goal-contract draft in flight.
type pendingDraft struct {
	ch     chan goalDraft
	cancel context.CancelCauseFunc
	start  time.Time
}

// goalDraft is the outcome of a concurrent contract draft.
type goalDraft struct {
	text string
	err  error
}

// startGoalDraft launches the goal-contract draft in the background so it
// overlaps exploration instead of serialising in front of the first pass. The
// pending draft's channel always receives exactly one value.
func (s *Session) startGoalDraft(ctx context.Context, pipe *rolemanager.Pipeline, clean string) *pendingDraft {
	cctx, cancel := context.WithCancelCause(ctx)
	p := &pendingDraft{ch: make(chan goalDraft, 1), cancel: cancel, start: time.Now()}
	commands := s.allTestCommands()
	go func() {
		dctx, stop := context.WithTimeout(cctx, goalDraftCeiling)
		defer stop()
		text, err := rolemanager.DraftGoalContract(dctx, pipe.Classifier, rolemanager.GoalDraftInput{
			Prompt:              clean,
			VerificationSurface: commands,
		})
		p.ch <- goalDraft{text: text, err: err}
	}()
	return p
}

// poll returns the draft's outcome without waiting. ok is false while the
// draft is still running.
func (p *pendingDraft) poll() (goalDraft, bool) {
	select {
	case d := <-p.ch:
		return d, true
	default:
		return goalDraft{}, false
	}
}

// joinGoalDraft returns the goal text the loop starts on, without waiting: the
// drafted contract when the draft is already done, else the raw prompt with
// pending true, and the draft keeps running for adoptLateGoalDraft.
func (s *Session) joinGoalDraft(p *pendingDraft, clean string, emit func(Event)) (text string, pending bool) {
	d, ok := p.poll()
	if !ok {
		return clean, true
	}
	return s.settleGoalDraft(p, d, clean, emit), false
}

// goalContractNote introduces a contract adopted after the loop started.
const goalContractNote = "The goal contract for this objective is ready. It restates the objective above; keep working from where you are, and use it to decide what remains and how to verify it."

// adoptLateGoalDraft takes a draft that finished after the goal loop started.
// A usable contract becomes the evaluator's goal text and is handed to the
// model as a sealed directive; a failed one is reported and dropped. It is a
// no-op while the draft is still running or when there is none.
func (s *Session) adoptLateGoalDraft(l *passLedger, clean string, emit func(Event)) []run.Turn {
	p := s.turnDraft
	if p == nil {
		return nil
	}
	d, ok := p.poll()
	if !ok {
		return nil
	}
	s.turnDraft = nil
	text := s.settleGoalDraft(p, d, clean, emit)
	if text == clean {
		return nil
	}
	l.goalText = text
	// The contract is sanitized, then harness-sealed — the same standing it
	// had when it rode in the sealed system block.
	return directiveTurns(goalContractNote + "\n\n" + text)
}

// settleGoalDraft turns a finished draft into goal text. Any failure —
// transport, timeout, or an unusable draft — fails open to the raw prompt: a
// weak or slow drafting model must never cost the turn.
func (s *Session) settleGoalDraft(p *pendingDraft, d goalDraft, clean string, emit func(Event)) string {
	p.cancel(nil)
	if d.err != nil {
		emit(Event{Kind: EventWarningKind, Warning: goalDraftFailure(d.err, time.Since(p.start))})
		return clean
	}
	drafted := sanitize.Sanitize(d.text)
	if strings.TrimSpace(drafted) == "" || !strings.Contains(drafted, clean) {
		emit(Event{Kind: EventWarningKind, Warning: "goal contract draft was unusable; carrying the raw prompt"})
		return clean
	}
	return drafted
}

// goalDraftFailure names why the draft failed, so the warning says whether to
// look at the model's speed, the provider, or the draft itself. It carries a
// provider's status code but never its response body: that is provider text.
func goalDraftFailure(err error, took time.Duration) string {
	var pe *run.ProviderError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Sprintf("goal contract draft timed out after %s; carrying the raw prompt", took.Round(time.Second))
	case errors.Is(err, rolemanager.ErrGoalDraftUnusable):
		return "goal contract draft was unusable; carrying the raw prompt"
	case errors.As(err, &pe) && pe.Status != 0:
		return fmt.Sprintf("goal contract drafting failed (provider returned %d); carrying the raw prompt", pe.Status)
	case errors.As(err, &pe):
		return "goal contract drafting failed (provider unreachable); carrying the raw prompt"
	}
	return "goal contract drafting failed; carrying the raw prompt"
}

// continuationRe matches a prompt that asks the harness to carry on with the
// work in flight rather than state a new objective.
var continuationRe = regexp.MustCompile(`^(please\s+)?(continue|resume|keep going|carry on|go on|proceed|keep at it)\b[\s,.:;!-]*(the\s+|with\s+the\s+|with\s+)?(goal|task|work|plan)?\b[\s,.:;!-]*`)

// maxContinuationLen keeps a long new instruction that merely starts with
// "continue" from being folded into the old goal: past this length the text
// is a new objective.
const maxContinuationLen = 240

// IsContinuation reports whether a goal-mode prompt is a request to resume
// the goal in flight. It is deterministic: no model call decides it.
func IsContinuation(prompt string) bool {
	p := strings.ToLower(strings.TrimSpace(prompt))
	if p == "" {
		return true
	}
	if len(p) > maxContinuationLen {
		return false
	}
	return continuationRe.MatchString(p)
}

// continuationExtra returns what a continuation prompt adds beyond the bare
// "continue", e.g. "continue, and keep the docs aligned" → "and keep the docs
// aligned". It returns "" for a bare continuation.
func continuationExtra(prompt string) string {
	p := strings.TrimSpace(prompt)
	loc := continuationRe.FindStringIndex(strings.ToLower(p))
	if loc == nil {
		return p
	}
	return strings.TrimSpace(p[loc[1]:])
}
