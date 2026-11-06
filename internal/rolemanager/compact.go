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
//
// focus is the user's one-line steer for a manual /compact. It is cleaned with
// sanitize.Line, capped at MaxCompactionFocusRunes and appended after the
// conversation as a labelled user-intent line. It never reaches the system
// text, carries no harness authority, and does not change the section contract.
func BuildCompactionPayload(conversation string, caveman bool, focus string) ClassifierPayload {
	user := conversation
	if line := CompactionFocusLine(focus); line != "" {
		user += "\n\n" + line
	}
	return ClassifierPayload{
		System:    withCavemanVoice(compactionSystemPrompt, caveman),
		User:      user,
		MaxTokens: ClassifierStructuredMaxTokens,
		UseCase:   UseCaseCompaction,
	}
}

// MaxCompactionFocusRunes caps the focus text of a manual /compact.
const MaxCompactionFocusRunes = 500

// CompactionFocusLine is the labelled line a focus becomes, or "" when the
// focus is empty after cleaning.
func CompactionFocusLine(focus string) string {
	clean := sanitize.Line(focus, MaxCompactionFocusRunes)
	if clean == "" {
		return ""
	}
	return "User focus for this summary (give it extra weight, keep every required section): " + clean
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
	s, fault := checkSections(raw, compactionHeadings, 0)
	switch fault {
	case sectionsEmpty:
		recordModel(EventCompactionSummary, "invalid", "", "empty", 0, model)
		return "", ErrIncompleteSummary
	case sectionsIncomplete, sectionsOversize:
		return "", ErrIncompleteSummary
	}
	recordModel(EventCompactionSummary, "valid", "", "", 0, model)
	return s, nil
}

// compactionHeadings are the headings a compaction summary must carry.
var compactionHeadings = []string{"## Goal", "## Next Steps", "## Critical Context"}

// sectionsFault names why checkSections refused a reply.
type sectionsFault string

const (
	sectionsOK         sectionsFault = ""
	sectionsEmpty      sectionsFault = "empty"
	sectionsIncomplete sectionsFault = "incomplete"
	sectionsOversize   sectionsFault = "oversize"
)

// checkSections is the shared body of the structured-prose validators (the
// compaction summary and the /relay handoff): the reply is trimmed, must not be
// empty, must carry every heading, must fit maxBytes when that is above zero,
// and is returned with delimiter markup removed so text embedded in the
// conversation cannot survive into what is stored. It never repairs a reply.
func checkSections(raw string, headings []string, maxBytes int) (string, sectionsFault) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", sectionsEmpty
	}
	for _, h := range headings {
		if !strings.Contains(s, h) {
			return "", sectionsIncomplete
		}
	}
	s = sanitize.Sanitize(s)
	if maxBytes > 0 && len(s) > maxBytes {
		return "", sectionsOversize
	}
	return s, sectionsOK
}

// SummaryPrefix/Suffix wrap a validated summary when it re-enters as a
// synthetic user turn; SummaryAck is the synthetic assistant acknowledgement
// that keeps the model from treating the summary as the request to answer.
const (
	SummaryPrefix = "The earlier part of this session was summarised to save context. This is that summary, not a new request:\n\n"
	SummarySuffix = "\n\nContinue the work from here. Do not restate the summary and do not re-introduce yourself."
	SummaryAck    = "Understood. I have the summarised context and will continue from there."
)
