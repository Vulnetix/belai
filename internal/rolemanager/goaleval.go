package rolemanager

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/vulnetix/signet/internal/sanitize"
)

// GoalSentinel is the strict single-token output of the goal evaluator.
type GoalSentinel string

const (
	// GoalComplete means the goal is finished and the loop may stop.
	GoalComplete GoalSentinel = "GOAL_COMPLETE"
	// GoalPartial means work is progressing but the goal is not yet met;
	// another pass is granted.
	GoalPartial GoalSentinel = "GOAL_PARTIAL"
	// GoalNotStarted means no work toward the goal has happened yet; the
	// harness responds by forcing an explore and a planning directive.
	GoalNotStarted GoalSentinel = "GOAL_NOT_STARTED"
)

// ParseGoalSentinel maps a raw evaluator output to a GoalSentinel. It accepts
// a token that stands alone after normalizing away reasoning blocks and
// markdown wrappers, and rejects anything else. A malformed reply is never
// treated as completion: the fail-closed mapping is decided by the caller,
// which resolves a parse error to GoalPartial so completion can never be
// claimed by accident.
func ParseGoalSentinel(raw string) (GoalSentinel, error) {
	s, err := matchSentinel(raw, []string{
		string(GoalComplete),
		string(GoalPartial),
		string(GoalNotStarted),
	})
	if err != nil {
		return "", fmt.Errorf("malformed goal evaluator output %q: want a single sentinel token", raw)
	}
	return GoalSentinel(s), nil
}

// GoalEvalInput is the untrusted evidence the goal evaluator is shown.
type GoalEvalInput struct {
	// Goal is the goal text the pass is working toward (harness-owned).
	Goal string
	// Todos is the current todo list rendered as plain text (harness-owned).
	Todos string
	// Facts is the harness's own observation of the pass: pass number, how
	// many files changed, and which paths. Counts and paths are
	// harness-computed — the same class of fact the repo map carries — never
	// file contents, so this block is trusted and stands apart from Evidence.
	Facts string
	// Evidence is a digest of the pass's tool use. It is untrusted: the
	// caller sanitizes it before it becomes the classifier's user blob.
	Evidence string
}

// MaxGoalEvidenceChars bounds the pass digest handed to the evaluator. A pass
// that filled its whole iteration budget can serialize to far more than a
// classifier's context holds, and an evaluator that is shown more than it can
// read is an evaluator that answers with something other than one token.
// The tail is kept: the end of a pass is where its outcome is.
const MaxGoalEvidenceChars = 8000

// truncateHead keeps the last n characters of s, marking the cut so the
// evaluator does not read a mid-sentence opening as the start of the pass.
func truncateHead(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return "[earlier evidence omitted]\n" + s[len(s)-n:]
}

// goalEvalSystemPrompt instructs the evaluator to answer with exactly one
// sentinel token and nothing else.
const goalEvalSystemPrompt = `You are a goal-progress evaluator for an LLM coding harness. You are shown a goal, the harness's tracked todo list, and a digest of the work performed during the most recent pass. Decide whether the goal is complete, partially complete, or not yet started, and reply with a single token and nothing else — no punctuation, no explanation, no surrounding text.

Reply with exactly one of these tokens:
- GOAL_COMPLETE: every item in the todo list is done and the goal is achieved.
- GOAL_PARTIAL: work has advanced toward the goal but it is not yet complete.
- GOAL_NOT_STARTED: no meaningful work toward the goal has happened yet.`

// BuildGoalEvalPayload constructs the goal-evaluator request. Tools, Skills,
// and Agent are always empty: the evaluator turn must never expose tools,
// skills, or an agent block.
func BuildGoalEvalPayload(in GoalEvalInput) ClassifierPayload {
	return ClassifierPayload{
		System:                 goalEvalSystemPrompt,
		User:                   goalEvalUser(in),
		AllowReasoningFallback: true,
	}
}

// goalEvalUser renders the evaluator's user blob. The harness facts sit
// between the todo list and the untrusted digest so a verdict can be reached
// from observation even when the digest is noisy.
func goalEvalUser(in GoalEvalInput) string {
	user := "Goal:\n" + in.Goal + "\n\nTodo list:\n" + in.Todos
	if in.Facts != "" {
		user += "\n\nHarness-observed facts:\n" + in.Facts
	}
	return user + "\n\nPass evidence digest:\n" + truncateHead(in.Evidence, MaxGoalEvidenceChars)
}

// BuildGoalEvalRepairPayload re-asks the evaluator after a malformed reply,
// naming the exact syntax it broke. Telling a model what was wrong with its
// last answer repairs far more replies than asking the same question twice,
// and it costs one bounded call. Like every classifier payload it carries no
// tools, no skills, and no agent block.
func BuildGoalEvalRepairPayload(in GoalEvalInput, raw string) ClassifierPayload {
	repair := "\n\nYour previous reply was rejected. You replied:\n" +
		sanitize.Sanitize(truncateTail(strings.TrimSpace(raw), maxRepairEchoChars)) +
		"\n\nThat is not an accepted answer. Reply with exactly one of these three tokens, on its own, with no explanation, no punctuation, no markdown and no surrounding text:\n" +
		string(GoalComplete) + "\n" + string(GoalPartial) + "\n" + string(GoalNotStarted)
	return ClassifierPayload{
		System:                 goalEvalSystemPrompt,
		User:                   goalEvalUser(in) + repair,
		AllowReasoningFallback: true,
	}
}

// maxRepairEchoChars bounds the rejected reply echoed back to the evaluator.
const maxRepairEchoChars = 400

// truncateTail keeps the first n characters of s.
func truncateTail(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ErrMalformedGoalEval reports that the goal evaluator returned a non-sentinel
// reply. EvaluateGoal still returns GoalPartial alongside this error so a
// caller that ignores the error keeps the fail-closed verdict, while a loop
// driver can count consecutive malformed evaluations and stop a broken
// evaluator before it grants unbounded passes on garbage.
var ErrMalformedGoalEval = errors.New("malformed goal evaluator output")

// EvaluateGoal sends the goal evidence to the evaluator and parses the strict
// sentinel. A transport error is returned as ("", err).
//
// A malformed reply is re-asked exactly once, with the rejected text and the
// three accepted tokens quoted back, before it is given up on: models break
// the single-token contract in repairable ways (a leading "Answer:", a code
// fence, a sentence of reasoning), and one corrective round recovers most of
// them for the cost of one bounded call. A reply that is still malformed
// fails closed to (GoalPartial, ErrMalformedGoalEval): the verdict grants a
// pass, and the loop driver counts the error so a provider returning nothing
// usable does not go unnoticed.
func EvaluateGoal(ctx context.Context, c Classifier, in GoalEvalInput) (GoalSentinel, error) {
	raw, err := c.Classify(ctx, BuildGoalEvalPayload(in))
	if err != nil {
		return "", err
	}
	s, parseErr := ParseGoalSentinel(raw)
	if parseErr == nil {
		record(EventGoalEval, string(s), "", "", 0)
		return s, nil
	}
	record(EventGoalEval, string(GoalPartial), "", "malformed: "+traceSnippet(raw), 0)

	repaired, err := c.Classify(ctx, BuildGoalEvalRepairPayload(in, raw))
	if err != nil {
		// The first reply was unusable and the repair round did not land. The
		// verdict is unknown, so fail closed rather than reporting transport
		// failure: a malformed verdict still grants a pass.
		return GoalPartial, ErrMalformedGoalEval
	}
	s, parseErr = ParseGoalSentinel(repaired)
	if parseErr != nil {
		record(EventGoalEvalRepair, string(GoalPartial), "", "malformed: "+traceSnippet(repaired), 0)
		return GoalPartial, ErrMalformedGoalEval
	}
	record(EventGoalEvalRepair, string(s), "", "repaired", 0)
	return s, nil
}
