package rolemanager

import (
	"context"
	"fmt"
	"regexp"
	"time"
)

// ModeSentinel is the strict single-token output of the operating-mode
// classifier. It is distinct from the security sentinels in sentinels.go.
type ModeSentinel string

const (
	ModeAgent        ModeSentinel = "AGENT"
	ModePlan         ModeSentinel = "PLAN"
	ModeGoal         ModeSentinel = "GOAL"
	ModeHandoff      ModeSentinel = "HANDOFF"
	ModeDebug        ModeSentinel = "DEBUG"
	ModeFanOut       ModeSentinel = "FANOUT"
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
		string(ModeHandoff),
		string(ModeDebug),
		string(ModeFanOut),
		string(ModeUndetermined),
	})
	if err != nil {
		return "", fmt.Errorf("malformed mode classifier output %q", raw)
	}
	return ModeSentinel(s), nil
}

// modeClassifierSystemPrompt instructs the classifier to answer with exactly
// one intent token and nothing else. The classifier is shown only the user
// prompt — never attachment contents or referenced files.
const modeClassifierSystemPrompt = `You are an operating-mode classifier for an LLM coding harness. The user's prompt does not explicitly name a mode. Classify it into exactly one intent and reply with a single token and nothing else — no punctuation, no explanation.

Reply with exactly one of these tokens:
- AGENT: a general interactive coding request that needs no formal plan and no tracked goal.
- PLAN: a read-only investigation that should first produce a step-by-step plan before any changes.
- GOAL: a specific objective to be tracked and completed.
- HANDOFF: the user wants an already-written plan in an attached file carried out now, step by step.
- DEBUG: the user wants to reproduce, isolate, and fix a bug or failure.
- FANOUT: the user wants several independent read-only investigations in parallel before acting.
- UNDETERMINED: you cannot confidently classify the prompt.`

// BuildModeClassifierPayload constructs the prompt-classifier request for a
// user prompt. It carries only the prompt text: no tools, no skills, no agent
// block, no attachment contents, no referenced files.
func BuildModeClassifierPayload(prompt string) ClassifierPayload {
	return ClassifierPayload{
		System:                 modeClassifierSystemPrompt,
		User:                   prompt,
		AllowReasoningFallback: true,
		UseCase:                UseCaseModeEval,
	}
}

// modeSentinelToIntent maps a classifier sentinel to an intent. HANDOFF is
// downgraded to AGENT when no plan-file attachment is present, so the LLM can
// never route a prompt into the handoff profile without harness proof of a
// plan.
func modeSentinelToIntent(s ModeSentinel, hasPlan bool) Intent {
	switch s {
	case ModePlan:
		return IntentPlan
	case ModeGoal:
		return IntentGoal
	case ModeHandoff:
		if hasPlan {
			return IntentHandoff
		}
		return IntentAgent
	case ModeDebug:
		return IntentDebug
	case ModeFanOut:
		return IntentFanOut
	default:
		return IntentAgent
	}
}

// ClassifyMode sends only the user prompt to the mode classifier. Malformed
// classifier output fails closed to ModeUndetermined, which engages default
// agent mode.
func ClassifyMode(ctx context.Context, c Classifier, prompt string) (ModeSentinel, error) {
	start := time.Now()
	raw, model, err := classifyServed(ctx, c, BuildModeClassifierPayload(prompt))
	took := time.Since(start)
	if err != nil {
		return "", err
	}
	s, err := ParseModeSentinel(raw)
	if err != nil {
		recordTimed(EventModeClassify, string(ModeUndetermined), "", "malformed: "+traceSnippet(raw), 0, model, took)
		return ModeUndetermined, nil
	}
	recordTimed(EventModeClassify, string(s), "", "", 0, model, took)
	return s, nil
}

// ClassifyModeIntent runs the LLM mode classifier and returns the resolved
// intent. A HANDOFF sentinel without a plan attachment resolves to AGENT.
func ClassifyModeIntent(ctx context.Context, c Classifier, prompt string, hasPlan bool) (Intent, string, error) {
	s, err := ClassifyMode(ctx, c, prompt)
	if err != nil {
		return "", "", err
	}
	return modeSentinelToIntent(s, hasPlan), string(s), nil
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
