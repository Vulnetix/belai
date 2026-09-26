package agentprofile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/sanitize"
)

const builderSystemPrompt = `You are an expert agent designer for a secure LLM coding harness. Given a user's request, design a named, reusable agent profile.

Reply with ONLY a JSON object matching this schema:
{
  "name": "string (filesystem-safe, required)",
  "description": "string (required)",
  "system_prompt": "string (required)",
  "tools": ["string array of known tool names, optional"],
  "mode": "one of: single, loop, scheduled, monitor (required)",
  "schedule": "cron-like string when mode is scheduled, optional otherwise",
  "monitor_condition": "human-readable trigger when mode is monitor, optional otherwise",
  "reflection": true or false (optional),
  "max_iterations": integer (optional),
  "autonomy": "supervised or autonomous (optional, default supervised)",
  "provider": "provider name to use, or omit to inherit from the session",
  "model": "model id to use, or omit to inherit from the session provider",
  "effort": "low, medium, high, or none (optional)",
  "guardrails": true or false or omit to inherit from settings (default inherit)",
  "ask_permission": true or false or omit to inherit from settings (default inherit)"
}

Before emitting the final JSON, reason through your choices inside a <thinking> block. Justify the tool allowlist explicitly.

The description and system_prompt must be meaningful and derived from the agent's name, even if the user only supplied a name. Infer the intended purpose from the name: describe what the agent does in one sentence, and write a system_prompt that defines its role, tone, scope, and default behavior for that inferred purpose. Avoid generic text such as "You are a helpful assistant"; tailor the prompt to the name.

Rules:
- Output ONLY valid JSON. No Markdown fences, no prose outside the JSON, no trailing text.
- name must be [a-zA-Z0-9._-]+.
- description and system_prompt must be specific and meaningful; derive them from the agent name when no other context is given.
- tools must only contain known names: Read, Write, Edit, Bash, Grep, Glob, WebFetch, WebSearch. Write and Edit mutate the workspace; only grant them when the agent's task genuinely needs to change files, and justify that grant explicitly.
- mode must be exactly one of the four allowed strings.
- autonomy must be "supervised" or "autonomous".`

// Builder uses a classifier LLM to generate AgentProfile JSON with
// validation error feedback.
type Builder struct {
	Classifier  rolemanager.Classifier
	MaxAttempts int
	// Caveman voices the designer prompt. The JSON contract is unaffected:
	// parseBuilderReply still requires valid JSON and Validate still runs.
	Caveman bool
	// OnAttempt, when non-nil, is called at the top of each design attempt so
	// callers can surface progress. note carries the previous attempt's
	// validation error on retries and is empty on the first attempt.
	OnAttempt func(attempt int, note string)
}

// Build runs the agent-designer loop, failing closed after MaxAttempts.
func (b *Builder) Build(ctx context.Context, userRequest string) (AgentProfile, error) {
	if b.Classifier == nil {
		return AgentProfile{}, errors.New("builder: no classifier configured")
	}
	maxAttempts := b.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}

	system := rolemanager.CavemanProse(builderSystemPrompt, b.Caveman)
	turns := []builderTurn{{Role: "user", Content: sanitize.Sanitize(userRequest)}}

	var note string
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if b.OnAttempt != nil {
			b.OnAttempt(attempt, note)
		}
		payload := rolemanager.ClassifierPayload{
			System:    system,
			User:      buildBuilderUserContent(turns),
			MaxTokens: rolemanager.ClassifierStructuredMaxTokens,
		}
		raw, err := b.Classifier.Classify(ctx, payload)
		if err != nil {
			return AgentProfile{}, fmt.Errorf("builder attempt %d: classifier error: %w", attempt, err)
		}

		profile, parseErr := parseBuilderReply(raw)
		if parseErr == nil {
			validationErr := profile.Validate()
			if validationErr == nil {
				return profile, nil
			}
			parseErr = validationErr
		}

		note = sanitize.Sanitize(fmt.Sprintf("validation error: %s", parseErr.Error()))
		feedback := sanitize.Sanitize(fmt.Sprintf("Validation error: %s. Please fix the profile and return only valid JSON.", parseErr.Error()))
		turns = append(turns, builderTurn{Role: "assistant", Content: raw})
		turns = append(turns, builderTurn{Role: "user", Content: feedback})
	}

	return AgentProfile{}, fmt.Errorf("builder failed after %d attempts", maxAttempts)
}

type builderTurn struct {
	Role    string
	Content string
}

func buildBuilderUserContent(turns []builderTurn) string {
	var sb strings.Builder
	for _, t := range turns {
		if t.Role == "user" {
			sb.WriteString("User: ")
		} else {
			sb.WriteString("Designer: ")
		}
		sb.WriteString(t.Content)
		sb.WriteString("\n\n")
	}
	return sb.String()
}

func parseBuilderReply(raw string) (AgentProfile, error) {
	clean := strings.TrimSpace(raw)
	clean = strings.TrimPrefix(clean, "```json")
	clean = strings.TrimPrefix(clean, "```")
	clean = strings.TrimSuffix(clean, "```")
	clean = strings.TrimSpace(clean)

	for {
		start := strings.Index(clean, "<thinking>")
		if start == -1 {
			break
		}
		end := strings.Index(clean[start:], "</thinking>")
		if end == -1 {
			clean = clean[:start]
			break
		}
		clean = clean[:start] + clean[start+end+len("</thinking>"):]
		clean = strings.TrimSpace(clean)
	}

	var p AgentProfile
	if err := json.Unmarshal([]byte(clean), &p); err != nil {
		return AgentProfile{}, fmt.Errorf("invalid JSON: %w", err)
	}
	return p, nil
}
