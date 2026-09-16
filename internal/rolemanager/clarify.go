package rolemanager

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/vulnetix/signet/internal/clarify"
	"github.com/vulnetix/signet/internal/sanitize"
)

// clarifySystemPrompt instructs the classifier to emit a clarification
// questionnaire as strict JSON.
const clarifySystemPrompt = `You are a clarification assistant for a secure LLM coding harness. The user sent an ambiguous prompt, and read-only exploration has produced some findings. Decide what still needs to be clarified, and reply with ONLY a JSON object matching this schema:

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
- groups may contain 1–6 items. Use {"groups": []} when there is nothing left to clarify.
- Each group must have 2–4 options. One option is not a choice.
- context must be a single line, ≤200 runes, and end with '.' or '?'.
- label must be non-empty, a single line, ≤80 runes, and unique within its group.
- description is optional; if given it must be a single line ≤160 runes.
- multi is optional and defaults to false.`

// ClarifyInput is the raw material for the clarifier prompt.
type ClarifyInput struct {
	Prompt   string
	Findings string
	Round    string
}

// BuildClarifyPayload constructs the classifier request for a clarification
// round. The payload carries no tools, no skills, and no agent block.
func BuildClarifyPayload(in ClarifyInput) ClassifierPayload {
	return ClassifierPayload{
		System:    clarifySystemPrompt,
		User:      buildClarifyUserContent(in.Prompt, in.Findings, in.Round),
		MaxTokens: ClassifierStructuredMaxTokens,
	}
}

// ErrClarifyUnusable is returned when the classifier cannot produce a valid
// questionnaire after the retry budget is exhausted.
var ErrClarifyUnusable = errors.New("clarifier output was not usable after retries")

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

	turns := []clarifyTurn{
		{role: "user", content: buildClarifyUserContent(in.Prompt, in.Findings, in.Round)},
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		payload := ClassifierPayload{
			System: clarifySystemPrompt,
			User:   renderClarifyTurns(turns),
		}
		raw, err := c.Classify(ctx, payload)
		if err != nil {
			return clarify.Questionnaire{}, fmt.Errorf("clarify attempt %d: %w", attempt, err)
		}

		q, parseErr := clarify.Parse(raw)
		if parseErr == nil {
			parseErr = q.Validate()
		}
		if parseErr == nil {
			return q, nil
		}

		feedback := sanitize.Sanitize(fmt.Sprintf("Validation error: %s. Reply with only valid JSON matching the schema.", parseErr.Error()))
		turns = append(turns, clarifyTurn{role: "assistant", content: raw})
		turns = append(turns, clarifyTurn{role: "user", content: feedback})
	}

	return clarify.Questionnaire{}, ErrClarifyUnusable
}

func buildClarifyUserContent(prompt, findings, round string) string {
	var b strings.Builder
	b.WriteString("Original prompt: ")
	b.WriteString(sanitize.Sanitize(prompt))
	b.WriteString("\nRound: ")
	b.WriteString(sanitize.Sanitize(round))
	b.WriteString("\nExploration findings:\n")
	b.WriteString(sanitize.Sanitize(findings))
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
