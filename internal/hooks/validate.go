// Package hooks validates hook definitions before they are loaded. Hooks load
// only after schema validation, and command paths are checked so a hook cannot
// inject an arbitrary code path (no absolute paths, no traversal, no shell
// metacharacters).
package hooks

import (
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
}

var allowedEvents = map[string]bool{
	"pre_tool":      true,
	"post_tool":     true,
	"session_start": true,
	"session_end":   true,
	"pre_edit":      true,
	"post_edit":     true,
}

// ParseHookFile parses and validates a hook definition (JSON). Malformed JSON
// or an invalid hook is rejected.
func ParseHookFile(data []byte) (*Hook, error) {
	var h Hook
	if err := json.Unmarshal(data, &h); err != nil {
		return nil, fmt.Errorf("invalid hook json: %w", err)
	}
	return ValidateHook(h)
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
