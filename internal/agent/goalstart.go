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

// The goal-contract draft has two bounds. Real sessions showed it holding the
// first pass back for minutes (once for close to three hours) behind a slow
// routed model, and the contract is an aid, not a prerequisite, so past either
// bound the goal starts on the raw prompt — the same fail-open path a failed
// draft already takes.
//
// goalDraftCeiling bounds the draft itself. goalDraftGrace bounds how long the
// goal loop waits for it once exploration is done and the contract is needed.
// The draft runs alongside exploration, so time spent exploring is free: a
// single deadline counted from the start killed a ~30s reasoning-model draft
// at 20s even while a minute of exploration was still running.
const goalDraftCeiling = 2 * time.Minute

// goalDraftGrace is a var so tests can shorten it.
var goalDraftGrace = 45 * time.Second

// errGoalDraftGrace cancels a draft still running when the grace expires. It
// wraps context.DeadlineExceeded so the draft's activity records a timeout.
var errGoalDraftGrace = fmt.Errorf("goal contract draft not ready in time: %w", context.DeadlineExceeded)

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

// joinGoalDraft waits for the draft and returns the goal text the loop runs
// against. Any failure — transport, timeout, or an unusable draft — fails open
// to the raw prompt: a weak or slow drafting model must never cost the turn.
func (s *Session) joinGoalDraft(p *pendingDraft, clean string, emit func(Event)) string {
	var d goalDraft
	select {
	case d = <-p.ch:
	case <-time.After(goalDraftGrace):
		// The loop has waited its grace: stop the draft and start on the raw
		// prompt. The draft's own activity row records the timeout.
		p.cancel(errGoalDraftGrace)
		d = goalDraft{err: errGoalDraftGrace}
	}
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
