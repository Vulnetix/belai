package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/vulnetix/belai/internal/clarify"
	"github.com/vulnetix/belai/internal/delimiters"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/tools"
)

// askUserUnavailable replaces the AskUserQuestion result when nobody can
// answer (a headless run, a subagent), so
// the model carries on rather than waiting for answers that never come.
const askUserUnavailable = "tool result withheld: no one can answer questions here. Decide from the code and sensible defaults, state your assumptions in the plan, and continue."

// canAskUser reports whether an AskUserQuestion call can reach a person: only
// in a top-level session, and only when the caller answers asks (the TUI, or
// an ACP editor).
func (s *Session) canAskUser() bool {
	return !s.exploreSubagent && (s.allowClarify || s.allowAsk)
}

// handleAskUser decides what an AskUserQuestion call does. Nobody to ask:
// the model is told to proceed. Every question already asked this session
// (by the model or the harness clarifier): the model is told to use the
// earlier answers. Otherwise agent and goal mode ask at once and the answers
// are the result, while plan mode keeps the sentinel and returns the
// questionnaire so the pass ends and the answers start a new agent turn.
func (s *Session) handleAskUser(ctx context.Context, pipe *rolemanager.Pipeline, args map[string]any, mode modes.Mode, emit func(Event)) (string, *clarify.Questionnaire) {
	if !s.canAskUser() {
		return askUserUnavailable, nil
	}
	q, err := tools.QuestionnaireFromArgs(args)
	if err != nil {
		return fmt.Sprintf("tool call rejected: %v", err), nil
	}
	q = s.dropAskedBefore(q)
	if q.Empty() {
		return askUserRepeated, nil
	}
	s.markAsked(q)
	if mode == modes.ModePlan {
		return tools.AskUserSentinel, &q
	}
	return s.askInline(ctx, pipe, q, emit), nil
}

// askUserRepeated is the result when every question was asked before.
const askUserRepeated = "tool result withheld: every one of these questions was already asked this session. Use the answers already in the conversation, or decide from the code and state your assumption."

// dropAskedBefore removes questions already asked this session.
func (s *Session) dropAskedBefore(q clarify.Questionnaire) clarify.Questionnaire {
	s.askedMu.Lock()
	defer s.askedMu.Unlock()
	return dropAsked(q, s.asked)
}

// markAsked records q's questions as asked this session.
func (s *Session) markAsked(q clarify.Questionnaire) {
	s.askedMu.Lock()
	defer s.askedMu.Unlock()
	if s.asked == nil {
		s.asked = map[string]bool{}
	}
	for _, g := range q.Groups {
		s.asked[clarifyKey(g.Context)] = true
	}
}

// askInline asks the questions now and returns the admitted answers as the
// tool result. The answers are the user's words, so the prompt classifier
// admits them first; a refusal or a declined questionnaire returns a note so
// the model proceeds on its own judgement.
func (s *Session) askInline(ctx context.Context, pipe *rolemanager.Pipeline, q clarify.Questionnaire, emit func(Event)) string {
	ans, ok := s.askUser(ctx, q, emit)
	if !ok || ans.SkippedAll() {
		return askUserDeclined
	}
	turn, ok := s.admitAnswers(ctx, pipe, ans.Render(q), emit)
	if !ok {
		return "tool result withheld: the answers could not be admitted. Proceed on your own judgement and state your assumptions."
	}
	return delimiters.Egress(turn.Content, s.pool)
}

// askUserDeclined is the result when the user skipped every question.
const askUserDeclined = "The user declined to answer. Proceed on your own judgement and state your assumptions."

// runWithClarify runs a turn and, when a plan-mode turn ended by asking the
// user (AskUserQuestion), shows the questions and runs the answers as a new
// agent-mode turn with the full tool surface, so the model carries the work
// forward instead of stopping at a plan. The answers are admitted by the
// prompt classifier like any prompt. Declined or unanswered questions return
// the plan turn's own result, with Clarify still set.
func (s *Session) runWithClarify(ctx context.Context, history []run.Turn, in TurnInput, streaming bool, emit func(Event)) (run.Result, error) {
	res, err := s.runTurn(ctx, history, in, streaming, emit)
	if err != nil || res.Clarify == nil || s.exploreSubagent {
		return res, err
	}
	q := *res.Clarify
	ans, ok := s.askUser(ctx, q, emit)
	if !ok || ans.SkippedAll() {
		return res, nil
	}

	prompt := res.SanitizedPrompt
	if prompt == "" {
		prompt = sanitize.Sanitize(in.Prompt)
	}
	prior := append(append([]run.Turn(nil), history...),
		run.Turn{Role: "user", Content: prompt},
		run.Turn{Role: "assistant", Content: askedTurn(res.Reply, q)},
	)
	// The answers are a new agent turn: plan mode's read-only surface does
	// not carry over, whatever the mode chip says. The session's own value is
	// restored for the next turn.
	saved := s.planMode
	s.planMode = false
	defer func() { s.planMode = saved }()
	emit(Event{Kind: EventWarningKind, Warning: "answers received · continuing in agent mode with every tool"})
	return s.runTurn(ctx, prior, TurnInput{Prompt: ans.Render(q), ForceMode: modes.ModeAgent}, streaming, emit)
}

// askedTurn is the assistant turn the next loop sees: what the planner said,
// then the questions it asked, so the answers read in context.
func askedTurn(reply string, q clarify.Questionnaire) string {
	var b strings.Builder
	if strings.TrimSpace(reply) != "" {
		b.WriteString(strings.TrimSpace(reply))
		b.WriteString("\n\n")
	}
	b.WriteString("I asked the user:")
	for i, g := range q.Groups {
		fmt.Fprintf(&b, "\n%d. %s", i+1, g.Context)
		for _, o := range g.Options {
			fmt.Fprintf(&b, "\n   - %s", o.Label)
		}
	}
	return b.String()
}
