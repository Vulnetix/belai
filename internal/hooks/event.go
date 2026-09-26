package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// Input is the JSON object a hook receives on stdin.
type Input struct {
	Event             string         `json:"event"`
	SessionID         string         `json:"session_id,omitempty"`
	Cwd               string         `json:"cwd,omitempty"`
	ToolName          string         `json:"tool_name,omitempty"`
	ToolInput         map[string]any `json:"tool_input,omitempty"`
	ToolResultSummary string         `json:"tool_result_summary,omitempty"`
	// Prompt is set for user_prompt_submit only.
	Prompt string `json:"prompt,omitempty"`
	// Notification is the harness-composed notification name
	// (permission, turn_done, …) for the notification event.
	Notification string `json:"notification,omitempty"`
	// Subagent is the finished subagent's id for subagent_stop.
	Subagent string `json:"subagent_id,omitempty"`
}

// Output is the JSON object a hook may print on stdout.
type Output struct {
	Decision          string `json:"decision,omitempty"`
	Reason            string `json:"reason,omitempty"`
	AdditionalContext string `json:"additional_context,omitempty"`
}

// Decisions a hook may return.
const (
	DecisionNone  = ""
	DecisionAllow = "allow"
	DecisionDeny  = "deny"
	DecisionAsk   = "ask"
)

// MaxContextBytes caps one hook's additional_context and reason.
const MaxContextBytes = 2 * 1024

// Blocking reports whether a hook for event can stop what fires it. A blocking
// hook that fails (nonzero exit, timeout, unparseable stdout) denies.
func Blocking(event string) bool {
	switch event {
	case EventUserPromptSubmit, EventPreTool, EventPreEdit:
		return true
	}
	return false
}

// Matches reports whether h applies to toolName. An empty matcher, or an
// event with no tool, matches.
func (h *Hook) Matches(toolName string) bool {
	if h.Matcher == "" || toolName == "" {
		return true
	}
	name := strings.ToLower(toolName)
	for _, p := range strings.Split(h.Matcher, "|") {
		if ok, _ := filepath.Match(strings.ToLower(strings.TrimSpace(p)), name); ok {
			return true
		}
	}
	return false
}

// Note is text a hook wrote for the model, attributed to the hook by name.
// It is untrusted: callers must sanitize and classify it before promotion.
type Note struct {
	Hook string
	Text string
}

// Outcome aggregates every hook that ran for one event.
type Outcome struct {
	// Decision is deny if any hook denied (or a blocking hook failed), else
	// ask if any asked, else allow if any allowed, else none.
	Decision string
	// DeniedBy names the first hook that denied.
	DeniedBy string
	// Reasons are the deny or ask reasons, in hook order.
	Reasons []Note
	// Context is each hook's additional_context, in hook order.
	Context []Note
	// Failures are hooks that errored; for a non-blocking event they are
	// reported and otherwise ignored.
	Failures []error
	// Ran is how many hooks ran.
	Ran int
}

// Set is the loaded hooks plus the runner that executes them.
type Set struct {
	Hooks  []*Hook
	Runner *Runner
}

// Empty reports whether no hook would ever run.
func (s *Set) Empty() bool { return s == nil || len(s.Hooks) == 0 || s.Runner == nil }

// Has reports whether any hook is registered for event.
func (s *Set) Has(event string) bool {
	if s.Empty() {
		return false
	}
	for _, h := range s.Hooks {
		if h.Event == event {
			return true
		}
	}
	return false
}

// Dispatch runs every hook registered for in.Event that matches in.ToolName,
// in name order, and aggregates their answers. Hooks run sequentially so a
// deny from an earlier hook is not raced by a later one's side effects; a
// deny does not stop later hooks from running.
func (s *Set) Dispatch(ctx context.Context, in Input) Outcome {
	var o Outcome
	if s.Empty() {
		return o
	}
	payload, err := json.Marshal(in)
	if err != nil {
		o.Failures = append(o.Failures, err)
		if Blocking(in.Event) {
			o.Decision = DecisionDeny
		}
		return o
	}
	blocking := Blocking(in.Event)
	var asked, allowed bool
	for _, h := range s.Hooks {
		if h.Event != in.Event || !h.Matches(in.ToolName) {
			continue
		}
		o.Ran++
		stdout, _, err := s.Runner.runStdin(ctx, *h, payload)
		out, perr := parseOutput(stdout)
		if err == nil && perr != nil {
			err = fmt.Errorf("hook %q: %w", h.Name, perr)
		}
		if err != nil {
			o.Failures = append(o.Failures, err)
			if blocking {
				o.deny(h.Name, "hook failed: "+err.Error())
			}
			continue
		}
		if out.AdditionalContext != "" {
			o.Context = append(o.Context, Note{Hook: h.Name, Text: clip(out.AdditionalContext)})
		}
		if !blocking {
			continue
		}
		switch out.Decision {
		case DecisionDeny:
			o.deny(h.Name, out.Reason)
		case DecisionAsk:
			asked = true
			if out.Reason != "" {
				o.Reasons = append(o.Reasons, Note{Hook: h.Name, Text: clip(out.Reason)})
			}
		case DecisionAllow:
			allowed = true
		}
	}
	if o.Decision != DecisionDeny {
		switch {
		case asked:
			o.Decision = DecisionAsk
		case allowed:
			o.Decision = DecisionAllow
		}
	}
	return o
}

func (o *Outcome) deny(name, reason string) {
	if o.Decision != DecisionDeny {
		o.Decision = DecisionDeny
		o.DeniedBy = name
	}
	if reason != "" {
		o.Reasons = append(o.Reasons, Note{Hook: name, Text: clip(reason)})
	}
}

// parseOutput reads a hook's stdout. Empty stdout is no opinion. Anything
// else must be exactly one JSON object with a known decision.
func parseOutput(stdout string) (Output, error) {
	var out Output
	s := strings.TrimSpace(stdout)
	if s == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return Output{}, fmt.Errorf("stdout is not a JSON decision: %w", err)
	}
	switch out.Decision {
	case DecisionNone, DecisionAllow, DecisionDeny, DecisionAsk:
	default:
		return Output{}, fmt.Errorf("unknown decision %q", out.Decision)
	}
	return out, nil
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= MaxContextBytes {
		return s
	}
	cut := MaxContextBytes
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
