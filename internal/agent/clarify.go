package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/vulnetix/signet/internal/clarify"
	"github.com/vulnetix/signet/internal/explore"
	"github.com/vulnetix/signet/internal/prompt"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/sanitize"
)

// clarifyRounds runs the clarify loop up to the configured round cap. Each
// round asks the user a questionnaire and admits the rendered answers through
// the Role Manager. A cancelled or refused answer set stops the loop so the
// harness never guesses on ambiguous input.
//
// seed is the explore wave's findings. With findings, each answer is followed
// up by a bounded read-only fan-out (explore→clarify→explore). Without them —
// plan mode with the survey off, the default — the clarifier works from the
// harness-computed workspace facts and the answers go straight to the planner,
// which reads what it needs; a fan-out per answer would hold planning back for
// minutes.
//
// Every round sees the questions already asked and their answers, and a
// question the user has already answered is dropped rather than asked again.
// The workspace facts stay in the evidence every round: when a follow-up
// fan-out replaced them, later rounds lost the file list and offered files
// that do not exist.
//
// It returns the admitted answers — the user's own words, which the caller
// attaches to the user's prompt turn so the planner treats them as direction,
// never as exploration evidence — and the follow-up exploration turns.
func (s *Session) clarifyRounds(ctx context.Context, pipe *rolemanager.Pipeline, decision rolemanager.ModeDecision, clean string, seed []run.Turn, emit func(Event)) (answers []string, out []run.Turn) {
	if !s.allowClarify {
		return nil, nil
	}
	maxRounds := s.settings.Resilience.MaxClarifyRoundsOr(3)
	if maxRounds < 0 {
		return nil, nil
	}

	followUp := len(seed) > 0
	facts := s.clarifyFacts()
	findings := joinEvidence(facts, digestClarifyFindings(seed))
	asked := map[string]bool{}
	var answered []string
	for round := 1; round <= maxRounds; round++ {
		emit(Event{Kind: EventRoleManagerKind, Phase: RoleManagerPhaseClarify})

		q, err := rolemanager.AskClarify(ctx, pipe.Classifier, rolemanager.ClarifyInput{
			Prompt:   clean,
			Findings: findings,
			Round:    fmt.Sprintf("%d", round),
			Answered: strings.Join(answered, "\n"),
		}, 3)
		if err != nil {
			break
		}
		q = dropAsked(q, asked)
		if q.Empty() {
			break
		}
		for _, g := range q.Groups {
			asked[clarifyKey(g.Context)] = true
		}

		ans, ok := s.askUser(ctx, q, emit)
		if !ok {
			break
		}
		// Declining every question carries no new information: re-asking the
		// same or a weaker questionnaire costs another clarify model call and
		// another explore wave without changing the plan, so stop asking.
		if ans.SkippedAll() {
			break
		}

		rendered := ans.Render(q)
		turn, ok := s.admitAnswers(ctx, pipe, rendered, emit)
		if !ok {
			break
		}
		answers = append(answers, turn.Content)
		answered = append(answered, rendered)

		if followUp {
			next := s.runExploreTasks(ctx, explore.PlanClarified(clean, q, ans), "clarify-explore", pipe, emit)
			out = append(out, next...)
			if f := digestClarifyFindings(next); f != "" {
				findings = joinEvidence(facts, f)
			}
		}
	}
	return answers, out
}

// joinEvidence joins the non-empty evidence parts.
func joinEvidence(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, "\n\n")
}

// clarifyFacts is the clarifier's evidence when no explore wave ran: the repo
// map and the top-level directory listing. Both are harness-computed (paths,
// counts, detected commands), never repository file contents, so the options
// the clarifier offers can name real files without a model reading any.
func (s *Session) clarifyFacts() string {
	var parts []string
	if s.repoMap != nil {
		if m := prompt.RepoMapBlock(*s.repoMap); m != "" {
			parts = append(parts, m)
		}
	}
	if l := listLayout(s.workdir); l != "" {
		parts = append(parts, "Top-level entries of the working directory:\n"+l)
	}
	return sanitize.Sanitize(strings.Join(parts, "\n\n"))
}

// dropAsked removes the groups whose question was already asked this turn. A
// clarifier that ignores its "already answered" list must not put the same
// question in front of the user again.
func dropAsked(q clarify.Questionnaire, asked map[string]bool) clarify.Questionnaire {
	kept := q.Groups[:0:0]
	for _, g := range q.Groups {
		if !asked[clarifyKey(g.Context)] {
			kept = append(kept, g)
		}
	}
	q.Groups = kept
	return q
}

// clarifyKey normalises a question for the repeat check: case, surrounding
// space and the closing punctuation do not make a question new.
func clarifyKey(context string) string {
	k := strings.ToLower(strings.Join(strings.Fields(context), " "))
	return strings.TrimRight(k, ".?! ")
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
