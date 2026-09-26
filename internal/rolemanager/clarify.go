package rolemanager

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/vulnetix/belai/internal/clarify"
	"github.com/vulnetix/belai/internal/sanitize"
)

// clarifySystemPrompt instructs the classifier to emit a clarification
// questionnaire as strict JSON. It is the planner framing: the classifier is
// asked, after exploration, whether it can proceed or still needs the user.
const clarifySystemPrompt = `You are a planning assistant for a secure LLM coding harness. The user sent a prompt, and the evidence below describes the workspace (harness-computed facts or read-only exploration findings). Decide whether you can proceed to planning without further user input, or whether you still need the user to answer a clarification questionnaire.

Reply with ONLY a JSON object matching this schema:

{
  "groups": [
    {
      "context": "one concise sentence ending with . or ?",
      "multi": false,
      "options": [
        {"label": "short option text", "description": "optional one-line hint"}
      ]
    }
  ]
}

Rules:
- Reply with ONLY valid JSON. No Markdown fences, no prose outside the JSON, no trailing text.
- groups may contain 1–6 items. Use {"groups": []} when you can proceed without the user.
- Prefer exactly one question; never exceed three groups.
- Never ask what a read-only tool could already answer from the evidence or the repository.
- Never repeat or rephrase a question listed under "Already answered". The user's answers there are final; when they settle the ambiguity, reply {"groups": []}.
- Base options on the evidence: name real files, directories or commands from it rather than generic placeholders.
- Each group must have 2–4 options. One option is not a choice.
- Put the recommended option first and suffix its label with " (Recommended)".
- Do not emit an "Other" option — the UI provides the free-form path itself.
- context must be a single line, ≤200 runes, and end with '.' or '?'.
- label must be non-empty, a single line, ≤80 runes, and unique within its group.
- description is optional; if given it must be a single line ≤160 runes.
- multi is optional and defaults to false.`

// ClarifyInput is the raw material for the clarifier prompt.
type ClarifyInput struct {
	Prompt   string
	Findings string
	Round    string
	// Answered is the rendered questions and answers of the earlier rounds of
	// this turn. Without it every round saw the same prompt and findings, so
	// the clarifier asked the same question again.
	Answered string
}

// BuildClarifyPayload constructs the classifier request for a clarification
// round. The payload carries no tools, no skills, and no agent block.
func BuildClarifyPayload(in ClarifyInput) ClassifierPayload {
	return ClassifierPayload{
		System:    clarifySystemPrompt,
		User:      buildClarifyUserContent(in),
		MaxTokens: ClassifierStructuredMaxTokens,
		UseCase:   UseCaseClarify,
	}
}

// ErrClarifyUnusable is returned when the classifier cannot produce a valid
// questionnaire after the retry budget is exhausted.
var ErrClarifyUnusable = errors.New("clarifier output was not usable after retries")

// ShouldClarify asks the planner classifier whether the user still needs to
// answer a clarification questionnaire after exploration. It returns proceed
// when the classifier produced an empty questionnaire (or could not produce a
// usable one — fail open to planning with the evidence at hand). A non-empty
// questionnaire returns proceed=false with the questionnaire ready to render.
func ShouldClarify(ctx context.Context, c Classifier, in ClarifyInput, maxAttempts int) (proceed bool, q clarify.Questionnaire, err error) {
	q, err = AskClarify(ctx, c, in, maxAttempts)
	if err != nil {
		return true, clarify.Questionnaire{}, nil // fail open: plan with current evidence
	}
	return q.Empty(), q, nil
}

type clarifyTurn struct {
	role    string
	content string
}

// AskClarify asks the classifier for a clarification questionnaire, retrying
// with validation feedback up to maxAttempts. A validation failure or empty
// questionnaire yields ErrClarifyUnusable so the caller can fall back to no
// questions.
func AskClarify(ctx context.Context, c Classifier, in ClarifyInput, maxAttempts int) (clarify.Questionnaire, error) {
	if c == nil {
		return clarify.Questionnaire{}, errors.New("clarify: no classifier configured")
	}
	if maxAttempts <= 0 {
		maxAttempts = 3
	}

	base := BuildClarifyPayload(in)
	turns := []clarifyTurn{
		{role: "user", content: base.User},
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// The use case routes the call (routing.use_cases.clarify); the
		// payload used to be built without it, so the clarify route was
		// never taken.
		payload := base
		payload.User = renderClarifyTurns(turns)
		raw, model, err := classifyServed(ctx, c, payload)
		if err != nil {
			return clarify.Questionnaire{}, fmt.Errorf("clarify attempt %d: %w", attempt, err)
		}

		q, parseErr := clarify.Parse(raw)
		if parseErr == nil {
			parseErr = q.Validate()
		}
		if parseErr == nil {
			recordModel(EventClarify, "usable", "", fmt.Sprintf("round=%s groups=%d", in.Round, len(q.Groups)), 0, model)
			return q, nil
		}
		recordModel(EventClarify, "invalid", "", fmt.Sprintf("round=%s attempt=%d", in.Round, attempt), 0, model)

		feedback := sanitize.Sanitize(fmt.Sprintf("Validation error: %s. Reply with only valid JSON matching the schema.", parseErr.Error()))
		turns = append(turns, clarifyTurn{role: "assistant", content: raw})
		turns = append(turns, clarifyTurn{role: "user", content: feedback})
	}

	return clarify.Questionnaire{}, ErrClarifyUnusable
}

func buildClarifyUserContent(in ClarifyInput) string {
	var b strings.Builder
	b.WriteString("Original prompt: ")
	b.WriteString(sanitize.Sanitize(in.Prompt))
	b.WriteString("\nRound: ")
	b.WriteString(sanitize.Sanitize(in.Round))
	if a := strings.TrimSpace(in.Answered); a != "" {
		b.WriteString("\nAlready answered:\n")
		b.WriteString(sanitize.Sanitize(a))
	}
	b.WriteString("\nEvidence:\n")
	b.WriteString(sanitize.Sanitize(in.Findings))
	return b.String()
}

func renderClarifyTurns(turns []clarifyTurn) string {
	var b strings.Builder
	for _, t := range turns {
		if t.role == "user" {
			b.WriteString("User: ")
		} else {
			b.WriteString("Assistant: ")
		}
		b.WriteString(t.content)
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String())
}
