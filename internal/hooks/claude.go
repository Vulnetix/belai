package hooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// The dialect's protocol, for a hook read from a bundle (Hook.Dialect ==
// DialectClaude): the payload on stdin, and the answer on stdout and in the exit
// code, mapped onto Belai's deny/ask/allow. Whatever the script prints, a hook
// can only narrow: allow changes nothing, and a blocking hook that fails, times
// out or answers nonsense denies.

// claudeInput is the payload a bundle hook receives on stdin.
type claudeInput struct {
	SessionID      string         `json:"session_id,omitempty"`
	TranscriptPath string         `json:"transcript_path"`
	Cwd            string         `json:"cwd,omitempty"`
	HookEventName  string         `json:"hook_event_name"`
	PermissionMode string         `json:"permission_mode"`
	ToolName       string         `json:"tool_name,omitempty"`
	ToolInput      map[string]any `json:"tool_input,omitempty"`
	ToolUseID      string         `json:"tool_use_id,omitempty"`
	// ToolResponse carries the harness's own bounded summary of the tool's
	// output, not the output itself.
	ToolResponse map[string]any `json:"tool_response,omitempty"`
	Prompt       string         `json:"prompt,omitempty"`
	Message      string         `json:"message,omitempty"`
	SubagentID   string         `json:"subagent_id,omitempty"`
}

func claudePayload(h *Hook, in Input) ([]byte, error) {
	ci := claudeInput{
		SessionID:      in.SessionID,
		Cwd:            in.Cwd,
		HookEventName:  h.EventName,
		PermissionMode: "default",
		ToolName:       in.ToolName,
		ToolInput:      in.ToolInput,
		ToolUseID:      in.ToolUseID,
		Prompt:         in.Prompt,
		Message:        in.Notification,
		SubagentID:     in.Subagent,
	}
	if in.ToolResultSummary != "" {
		ci.ToolResponse = map[string]any{"summary": in.ToolResultSummary}
	}
	return json.Marshal(ci)
}

// claudeOutput is what a bundle hook may print on stdout. Unknown keys are
// ignored (the dialect grows), and none of them can widen what a hook does.
type claudeOutput struct {
	Decision          string `json:"decision"`
	Reason            string `json:"reason"`
	Continue          *bool  `json:"continue"`
	StopReason        string `json:"stopReason"`
	AdditionalContext string `json:"additional_context"`
	Specific          struct {
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
		AdditionalContext        string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// takesPlainContext are the events whose plain-text stdout is context for the
// model, as the dialect defines them.
func takesPlainContext(event string) bool {
	return event == EventUserPromptSubmit || event == EventSessionStart
}

// parseClaudeOutput reads a bundle hook's stdout. It never returns a decision
// beyond Belai's three, and updatedInput and systemMessage are never read.
func parseClaudeOutput(event, stdout string) (Output, error) {
	s := strings.TrimSpace(stdout)
	if s == "" {
		return Output{}, nil
	}
	if !strings.HasPrefix(s, "{") {
		if takesPlainContext(event) {
			return Output{AdditionalContext: s}, nil
		}
		if Blocking(event) {
			return Output{}, errors.New("stdout is not a JSON decision")
		}
		return Output{}, nil // noise on an event nobody waits for
	}
	var raw claudeOutput
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return Output{}, fmt.Errorf("stdout is not a JSON decision: %w", err)
	}
	out := Output{Reason: firstNonEmpty(raw.Specific.PermissionDecisionReason, raw.Reason, raw.StopReason)}
	out.AdditionalContext = firstNonEmpty(raw.Specific.AdditionalContext, raw.AdditionalContext)
	decide := func(d string) (string, error) {
		switch d {
		case "":
			return DecisionNone, nil
		case "allow", "approve":
			return DecisionAllow, nil
		case "deny", "block":
			return DecisionDeny, nil
		case "ask":
			return DecisionAsk, nil
		}
		return "", fmt.Errorf("unknown decision %q", d)
	}
	top, err := decide(raw.Decision)
	if err != nil {
		return Output{}, err
	}
	spec, err := decide(raw.Specific.PermissionDecision)
	if err != nil {
		return Output{}, err
	}
	switch {
	case top == DecisionDeny || spec == DecisionDeny:
		out.Decision = DecisionDeny
	case top == DecisionAsk || spec == DecisionAsk:
		out.Decision = DecisionAsk
	case top == DecisionAllow || spec == DecisionAllow:
		out.Decision = DecisionAllow
	}
	if raw.Continue != nil && !*raw.Continue && Blocking(event) {
		out.Decision = DecisionDeny
	}
	return out, nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// exitCode is the exit status err carries, or -1.
func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}
