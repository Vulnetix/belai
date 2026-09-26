package rolemanager

import (
	"errors"
	"strings"

	"github.com/vulnetix/belai/internal/sanitize"
)

// compactionSystemPrompt instructs the classifier to summarise a conversation
// without continuing it.
const compactionSystemPrompt = `You are a context summarization assistant. Your task is to read a conversation between a user and an AI assistant, then produce a structured summary following the exact format specified.

Do NOT continue the conversation. Do NOT respond to any questions in the conversation. ONLY output the structured summary.

Produce exactly these sections, in this order:
## Goal
## Constraints & Preferences
## Progress
### Done
### In Progress
### Blocked
## Key Decisions
## Next Steps
## Critical Context

Keep each section concise. Preserve exact file paths, function names, and error messages.`

// BuildCompactionPayload constructs the summarisation request for an
// already-serialized conversation. Tools, Skills, and Agent are always empty.
// caveman voices the summary; the heading contract ValidateSummary enforces is
// preserved either way.
func BuildCompactionPayload(conversation string, caveman bool) ClassifierPayload {
	return ClassifierPayload{
		System:    withCavemanVoice(compactionSystemPrompt, caveman),
		User:      conversation,
		MaxTokens: ClassifierStructuredMaxTokens,
		UseCase:   UseCaseCompaction,
	}
}

// ErrIncompleteSummary reports a summary lacking the required structure —
// typically a response truncated by a max-token limit.
var ErrIncompleteSummary = errors.New("compaction summary is incomplete")

// ValidateSummary fails closed on a summary that is empty or that lacks the
// required headings. It also sanitizes delimiter markup so tool output
// embedded in the conversation cannot survive into the stored summary.
func ValidateSummary(raw string) (string, error) { return ValidateServedSummary(raw, "") }

// ValidateServedSummary is ValidateSummary for a reply whose answering
// provider/model is known (see TrackServedModel).
func ValidateServedSummary(raw, model string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		recordModel(EventCompactionSummary, "invalid", "", "empty", 0, model)
		return "", ErrIncompleteSummary
	}
	if !strings.Contains(s, "## Goal") ||
		!strings.Contains(s, "## Next Steps") ||
		!strings.Contains(s, "## Critical Context") {
		return "", ErrIncompleteSummary
	}
	recordModel(EventCompactionSummary, "valid", "", "", 0, model)
	return sanitize.Sanitize(s), nil
}

// SummaryPrefix/Suffix wrap a validated summary when it re-enters as a
// synthetic user turn; SummaryAck is the synthetic assistant acknowledgement
// that keeps the model from treating the summary as the request to answer.
const (
	SummaryPrefix = "The earlier part of this session was summarised to save context. This is that summary, not a new request:\n\n"
	SummarySuffix = "\n\nContinue the work from here. Do not restate the summary and do not re-introduce yourself."
	SummaryAck    = "Understood. I have the summarised context and will continue from there."
)
