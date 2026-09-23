// Package jev implements the Role Manager Jev tool-call gate: a classifier
// that asks the TypeSafe/Jev model (on OpenRouter) whether a model-emitted
// tool call may execute, and reduces its reply to a strict single-token
// verdict. The gate turn carries no tools, skills, or agent block.
package jev

import (
	"context"
	"fmt"
	"strings"

	"github.com/vulnetix/signet/internal/rolemanager"
)

// Sentinel is the strict single-token verdict of the Jev tool-call gate.
type Sentinel string

const (
	// Allow means the tool call may execute.
	Allow Sentinel = "ALLOW"
	// Deny means the tool call must not execute.
	Deny Sentinel = "DENY"
	// Inconclusive means the gate could not decide (a refusal or malformed
	// reply). The caller is expected to fail closed.
	Inconclusive Sentinel = "INCONCLUSIVE"
)

// systemPrompt instructs the Jev model to decide whether a tool call may
// execute and to answer with exactly one token.
const systemPrompt = `You are a tool-call gatekeeper for an LLM coding harness. You will be shown a tool call an assistant wants to make (the tool name and its arguments). Decide whether the call is safe to execute and reply with a single token and nothing else — no punctuation, no explanation, no surrounding text.

Reply with exactly one of these tokens:
- ALLOW: the tool call is safe to execute.
- DENY: the tool call is unsafe and must not execute.
- INCONCLUSIVE: you cannot decide.`

// BuildPayload constructs the Jev gate request for one tool call. Tools,
// Skills, and Agent are always empty: the gate turn must never expose tools,
// skills, or an agent block.
func BuildPayload(toolCall string) rolemanager.ClassifierPayload {
	return rolemanager.ClassifierPayload{
		System: systemPrompt,
		User:   toolCall,
	}
}

// Classifier implements rolemanager.Classifier by routing the payload to the
// underlying Jev LLM and reducing its reply to a jev.Sentinel.
type Classifier struct {
	llm rolemanager.Classifier
}

// New wraps an LLM classifier (typically the OpenRouter typesafe/jev model) as
// a Jev tool-call gate.
func New(llm rolemanager.Classifier) *Classifier {
	return &Classifier{llm: llm}
}

// Classify implements rolemanager.Classifier. A transport error from the
// underlying LLM is an error; a reply that cannot be parsed as a verdict is
// INCONCLUSIVE, not an error, so the caller can fail closed.
func (c *Classifier) Classify(ctx context.Context, p rolemanager.ClassifierPayload) (string, error) {
	raw, err := c.llm.Classify(ctx, p)
	if err != nil {
		return "", err
	}
	s, err := ParseVerdict(raw)
	if err != nil {
		return string(Inconclusive), nil
	}
	return string(s), nil
}

// ParseVerdict maps a raw Jev reply to a Sentinel using the shared sentinel
// normalizer (rolemanager.NormalizeSentinelReply). It accepts the canonical
// ALLOW/DENY/INCONCLUSIVE tokens and the lowercase allowed/denied/refused forms
// the model may emit; anything else, including an ambiguous reply, is
// malformed.
func ParseVerdict(raw string) (Sentinel, error) {
	norm := rolemanager.NormalizeSentinelReply(raw)
	canonical := map[string]Sentinel{
		"allow":        Allow,
		"allowed":      Allow,
		"deny":         Deny,
		"denied":       Deny,
		"inconclusive": Inconclusive,
		"refused":      Inconclusive,
	}
	found := map[Sentinel]bool{}
	for _, line := range strings.Split(norm, "\n") {
		line = strings.ToLower(strings.TrimSpace(line))
		if line == "" {
			continue
		}
		if s, ok := canonical[line]; ok {
			found[s] = true
		}
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("no jev verdict token found in %q", raw)
	case 1:
		for s := range found {
			return s, nil
		}
	}
	return "", fmt.Errorf("ambiguous jev verdict %q: multiple tokens", raw)
}
