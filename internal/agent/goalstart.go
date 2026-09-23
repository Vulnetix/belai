package agent

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/sanitize"
)

// goalDraftTimeout bounds the goal-contract draft. Real sessions showed the
// draft holding the first pass back for minutes (once for close to three
// hours) behind a slow routed model; the contract is an aid, not a
// prerequisite, so past this bound the goal starts on the raw prompt — the
// same fail-open path a failed draft already takes.
const goalDraftTimeout = 20 * time.Second

// goalDraft is the outcome of a concurrent contract draft.
type goalDraft struct {
	text string
	err  error
}

// startGoalDraft launches the goal-contract draft in the background so it
// overlaps exploration instead of serialising in front of the first pass. The
// returned channel always receives exactly one value.
func (s *Session) startGoalDraft(ctx context.Context, pipe *rolemanager.Pipeline, clean string) chan goalDraft {
	ch := make(chan goalDraft, 1)
	commands := s.allTestCommands()
	go func() {
		dctx, cancel := context.WithTimeout(ctx, goalDraftTimeout)
		defer cancel()
		text, err := rolemanager.DraftGoalContract(dctx, pipe.Classifier, rolemanager.GoalDraftInput{
			Prompt:              clean,
			VerificationSurface: commands,
		})
		ch <- goalDraft{text: text, err: err}
	}()
	return ch
}

// joinGoalDraft waits for the draft and returns the goal text the loop runs
// against. Any failure — transport, timeout, or an unusable draft — fails open
// to the raw prompt: a weak or slow drafting model must never cost the turn.
func (s *Session) joinGoalDraft(ch chan goalDraft, clean string, emit func(Event)) string {
	d := <-ch
	if d.err != nil {
		emit(Event{Kind: EventWarningKind, Warning: "goal contract drafting failed; carrying the raw prompt"})
		return clean
	}
	drafted := sanitize.Sanitize(d.text)
	if strings.TrimSpace(drafted) == "" || !strings.Contains(drafted, clean) {
		emit(Event{Kind: EventWarningKind, Warning: "goal contract draft was unusable; carrying the raw prompt"})
		return clean
	}
	return drafted
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
