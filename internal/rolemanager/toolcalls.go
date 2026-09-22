package rolemanager

import (
	"fmt"
	"strings"
)

// ToolCall is a model-emitted tool invocation.
type ToolCall struct {
	ID      string
	Name    string
	Args    map[string]any
	RawArgs string // original JSON text; set when Args were not fully parsed
}

// ToolCallMismatchPolicy is the user-configured behavior when a model emits a
// tool call for a tool that was not present in the prompt.
type ToolCallMismatchPolicy string

const (
	// PolicyAbort refuses the whole turn when a mismatch is found (fail-closed).
	PolicyAbort ToolCallMismatchPolicy = "abort"
	// PolicyStrip drops mismatched tool calls and continues with the rest.
	PolicyStrip ToolCallMismatchPolicy = "strip"
	// PolicyIgnore forwards mismatched tool calls unchanged.
	PolicyIgnore ToolCallMismatchPolicy = "ignore"
)

// CheckToolCalls validates model tool calls against the tool names that were
// present in the prompt, applying the configured mismatch policy. It returns
// the (possibly filtered) calls and, for PolicyAbort with a mismatch, an error.
// An empty policy defaults to PolicyAbort.
func CheckToolCalls(calls []ToolCall, promptTools []string, policy ToolCallMismatchPolicy) ([]ToolCall, error) {
	if policy == "" {
		policy = PolicyAbort
	}
	known := make(map[string]bool, len(promptTools))
	for _, t := range promptTools {
		known[t] = true
	}
	var out []ToolCall
	for _, c := range calls {
		if known[c.Name] {
			out = append(out, c)
			continue
		}
		switch policy {
		case PolicyStrip:
			// drop the mismatched call
		case PolicyIgnore:
			out = append(out, c)
		default:
			record(EventToolCallMismatch, string(PolicyAbort), c.Name, "abort", 0)
			closest := closestTool(c.Name, promptTools)
			if closest != "" {
				return nil, fmt.Errorf("tool call %q is not present in prompt tools (policy=abort; closest available: %s)", c.Name, closest)
			}
			return nil, fmt.Errorf("tool call %q is not present in prompt tools (policy=abort)", c.Name)
		}
	}
	return out, nil
}

// closestTool returns the registered tool name that case-insensitively
// prefixes the requested name, or vice versa. A model truncating "GCloud" as
// "GC" gets the hint "closest available: GCloud".
func closestTool(name string, promptTools []string) string {
	n := strings.ToLower(name)
	var best string
	for _, t := range promptTools {
		lt := strings.ToLower(t)
		if strings.HasPrefix(n, lt) || strings.HasPrefix(lt, n) {
			if len(lt) > len(best) {
				best = t
			}
		}
	}
	return best
}
