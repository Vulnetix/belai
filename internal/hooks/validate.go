// Package hooks validates and runs user hook commands at fixed points in a
// session. Hooks load only after schema validation (unknown keys rejected),
// and command paths are checked so a hook cannot inject an arbitrary code
// path (no absolute paths, no traversal, no shell metacharacters). A hook
// receives one JSON event on stdin and may answer with one JSON decision on
// stdout; see event.go for the contract.
package hooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Hook is a validated hook definition.
type Hook struct {
	Name    string `json:"name"`
	Event   string `json:"event"`
	Command string `json:"command"`
	// Matcher narrows the tool events to tool names matching one of its
	// '|'-separated glob patterns (case-insensitive). Empty matches every
	// tool. It is ignored for events that carry no tool.
	Matcher string `json:"matcher,omitempty"`
	// TimeoutMS bounds one run. Zero means the runner default; anything above
	// MaxTimeoutMS is rejected at validation.
	TimeoutMS int `json:"timeout_ms,omitempty"`

	// Dir is the directory the command resolves against and runs in. It is
	// set by the loader (the hooks directory, or a plugin's root), never read
	// from the hook file.
	Dir string `json:"-"`
}

// Event names.
const (
	EventSessionStart     = "session_start"
	EventSessionEnd       = "session_end"
	EventUserPromptSubmit = "user_prompt_submit"
	EventPreTool          = "pre_tool"
	EventPostTool         = "post_tool"
	EventPreEdit          = "pre_edit"
	EventPostEdit         = "post_edit"
	EventStop             = "stop"
	EventPreCompact       = "pre_compact"
	EventSubagentStop     = "subagent_stop"
	EventNotification     = "notification"
)

// MaxTimeoutMS caps a hook's own timeout_ms.
const MaxTimeoutMS = 60_000

var allowedEvents = map[string]bool{
	EventSessionStart:     true,
	EventSessionEnd:       true,
	EventUserPromptSubmit: true,
	EventPreTool:          true,
	EventPostTool:         true,
	EventPreEdit:          true,
	EventPostEdit:         true,
	EventStop:             true,
	EventPreCompact:       true,
	EventSubagentStop:     true,
	EventNotification:     true,
}

// Events returns every valid event name.
func Events() []string {
	out := make([]string, 0, len(allowedEvents))
	for e := range allowedEvents {
		out = append(out, e)
	}
	return out
}

// ParseHookFile parses and validates a hook definition (JSON). Malformed JSON,
// an unknown key, or an invalid hook is rejected.
func ParseHookFile(data []byte) (*Hook, error) {
	h, err := decodeStrict(data)
	if err != nil {
		return nil, err
	}
	return ValidateHook(h)
}

// decodeStrict decodes one hook object and rejects keys the schema does not
// declare, so a typo cannot silently drop a matcher and widen a hook.
func decodeStrict(data []byte) (Hook, error) {
	var h Hook
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&h); err != nil {
		return Hook{}, fmt.Errorf("invalid hook json: %w", err)
	}
	return h, nil
}

// ValidateHook checks a hook's schema and command path.
func ValidateHook(h Hook) (*Hook, error) {
	if strings.TrimSpace(h.Name) == "" {
		return nil, errors.New("hook name is required")
	}
	if !allowedEvents[h.Event] {
		return nil, fmt.Errorf("unknown hook event %q", h.Event)
	}
	if err := validateCommandPath(h.Command); err != nil {
		return nil, err
	}
	if h.TimeoutMS < 0 || h.TimeoutMS > MaxTimeoutMS {
		return nil, fmt.Errorf("hook timeout_ms must be between 0 and %d", MaxTimeoutMS)
	}
	if err := validateMatcher(h.Matcher); err != nil {
		return nil, err
	}
	return &h, nil
}

// validateCommandPath rejects command paths that could inject arbitrary code
// paths: absolute paths, traversal, or shell metacharacters.
func validateCommandPath(cmd string) error {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return errors.New("hook command is required")
	}
	if filepath.IsAbs(cmd) {
		return errors.New("hook command must be a relative path")
	}
	if strings.ContainsAny(cmd, ";&|`$<>\"'") {
		return errors.New("hook command must not contain shell metacharacters")
	}
	clean := filepath.Clean(cmd)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return errors.New("hook command path escapes the hooks directory")
	}
	return nil
}

func validateMatcher(m string) error {
	if m == "" {
		return nil
	}
	for _, p := range strings.Split(m, "|") {
		p = strings.TrimSpace(p)
		if p == "" {
			return errors.New("hook matcher has an empty pattern")
		}
		if _, err := filepath.Match(p, ""); err != nil {
			return fmt.Errorf("hook matcher %q: %w", p, err)
		}
	}
	return nil
}
