package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/vulnetix/signet/internal/clarify"
	"github.com/vulnetix/signet/internal/explore"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/sanitize"
)

// clarifyRounds runs the explore→clarify→explore loop up to the configured
// round cap. Each round asks the user a questionnaire, admits the rendered
// answers through the Role Manager, and fans the resulting tasks out to
// bounded read-only subagents. A cancelled or refused answer set stops the
// loop so the harness never guesses on ambiguous input.
func (s *Session) clarifyRounds(ctx context.Context, pipe *rolemanager.Pipeline, decision rolemanager.ModeDecision, clean string, seed []run.Turn, emit func(Event)) []run.Turn {
	if !s.allowClarify {
		return nil
	}
	maxRounds := s.settings.Resilience.MaxClarifyRoundsOr(3)
	if maxRounds < 0 {
		return nil
	}

	var out []run.Turn
	findings := digestClarifyFindings(seed)
	for round := 1; round <= maxRounds; round++ {
		emit(Event{Kind: EventRoleManagerKind, Phase: RoleManagerPhaseClarify})

		q, err := rolemanager.AskClarify(ctx, pipe.Classifier, rolemanager.ClarifyInput{
			Prompt:   clean,
			Findings: findings,
			Round:    fmt.Sprintf("%d", round),
		}, 3)
		if err != nil || q.Empty() {
			break
		}

		ans, ok := s.askUser(ctx, q, emit)
		if !ok {
			break
		}

		turn, ok := s.admitAnswers(ctx, pipe, ans.Render(q), emit)
		if !ok {
			break
		}

		out = append(out, turn)
		next := s.runExploreTasks(ctx, explore.PlanClarified(clean, q, ans), "clarify-explore", pipe, emit)
		out = append(out, next...)
		findings = digestClarifyFindings(next)
	}
	return out
}

// askUser emits a questionnaire event and blocks until the UI answers or the
// context is cancelled. It is the only agent→UI round-trip in the codebase:
// everything else is fire-and-forget.
func (s *Session) askUser(ctx context.Context, q clarify.Questionnaire, emit func(Event)) (clarify.Answers, bool) {
	reply := make(chan clarify.Answers, 1)
	emit(Event{Kind: EventClarifyAskKind, Clarify: &q, Reply: reply})
	select {
	case a := <-reply:
		return a, true
	case <-ctx.Done():
		return clarify.Answers{}, false
	}
}

// admitAnswers reuses the drainSteer admission discipline: answers re-enter as
// a plain user turn, never as a harness block, and a refusal drops the answers
// and stops the clarify loop.
func (s *Session) admitAnswers(ctx context.Context, pipe *rolemanager.Pipeline, text string, emit func(Event)) (run.Turn, bool) {
	clean := sanitize.Sanitize(text)
	emit(Event{Kind: EventRoleManagerKind, Phase: RoleManagerPhaseSteer})
	dec, err := pipe.Admit(ctx, clean, "your answers", s.live.Policy())
	if err != nil {
		emit(Event{Kind: EventErrorKind, Err: err})
		return run.Turn{}, false
	}
	if dec.Action != rolemanager.ActionProceed {
		emit(Event{Kind: EventErrorKind, Err: &rolemanager.RefusalError{Sentinel: dec.Sentinel}})
		return run.Turn{}, false
	}
	return run.Turn{Role: "user", Content: dec.Content}, true
}

func digestClarifyFindings(turns []run.Turn) string {
	var parts []string
	for _, t := range turns {
		if t.Content != "" {
			parts = append(parts, t.Content)
		}
	}
	return sanitize.Sanitize(strings.Join(parts, "\n"))
}
