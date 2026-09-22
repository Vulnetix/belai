package rolemanager

import (
	"context"
	"fmt"
	"regexp"
)

// ModeSentinel is the strict single-token output of the operating-mode
// classifier. It is distinct from the security sentinels in sentinels.go.
type ModeSentinel string

const (
	ModeAgent        ModeSentinel = "AGENT"
	ModePlan         ModeSentinel = "PLAN"
	ModeGoal         ModeSentinel = "GOAL"
	ModeUndetermined ModeSentinel = "UNDETERMINED"
)

// ParseModeSentinel maps a raw classifier output to a ModeSentinel. It accepts
// a token that stands alone after normalizing away reasoning blocks and
// markdown wrappers, and rejects anything else.
func ParseModeSentinel(raw string) (ModeSentinel, error) {
	s, err := matchSentinel(raw, []string{
		string(ModeAgent),
		string(ModePlan),
		string(ModeGoal),
		string(ModeUndetermined),
	})
	if err != nil {
		return "", fmt.Errorf("malformed mode classifier output %q", raw)
	}
	return ModeSentinel(s), nil
}

// modeClassifierSystemPrompt instructs the classifier to answer with exactly
// one mode token and nothing else. The classifier is shown only the user
// prompt — never attachment contents or referenced files.
const modeClassifierSystemPrompt = `You are an operating-mode classifier for an LLM coding harness. The user's prompt does not explicitly name a mode. Classify it into exactly one mode and reply with a single token and nothing else — no punctuation, no explanation.

Reply with exactly one of these tokens:
- AGENT: a general interactive coding request that needs no formal plan and no tracked goal.
- PLAN: a read-only investigation that should first produce a step-by-step plan before any changes.
- GOAL: a specific objective to be tracked and completed.
- UNDETERMINED: you cannot confidently classify the prompt.`

// BuildModeClassifierPayload constructs the prompt-classifier request for a
// user prompt. It carries only the prompt text: no tools, no skills, no agent
// block, no attachment contents, no referenced files.
func BuildModeClassifierPayload(prompt string) ClassifierPayload {
	return ClassifierPayload{
		System:                 modeClassifierSystemPrompt,
		User:                   prompt,
		AllowReasoningFallback: true,
	}
}

// ClassifyMode sends only the user prompt to the mode classifier. Malformed
// classifier output fails closed to ModeUndetermined, which engages default
// agent mode.
func ClassifyMode(ctx context.Context, c Classifier, prompt string) (ModeSentinel, error) {
	raw, err := c.Classify(ctx, BuildModeClassifierPayload(prompt))
	if err != nil {
		return "", err
	}
	s, err := ParseModeSentinel(raw)
	if err != nil {
		record(EventModeClassify, string(ModeUndetermined), "", "malformed: "+traceSnippet(raw), 0)
		return ModeUndetermined, nil
	}
	record(EventModeClassify, string(s), "", "", 0)
	return s, nil
}

// agentNameRe matches a named-agent reference of the form "@agent:NAME".
var agentNameRe = regexp.MustCompile(`@agent:([a-zA-Z0-9._-]+)`)

// ExtractAgentName returns the named agent referenced in a prompt via
// "@agent:NAME", or "" when none is present.
func ExtractAgentName(prompt string) string {
	if m := agentNameRe.FindStringSubmatch(prompt); m != nil {
		return m[1]
	}
	return ""
}
