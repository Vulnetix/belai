package tui

import (
	"strings"

	"github.com/vulnetix/signet/internal/run"
)

// normaliseTurns makes a turn list provider-safe: adjacent user turns merge
// (blank-line joined, the last turn's Attachments/Directive win), assistant
// turns with neither text nor calls are dropped, and tool turns whose call is
// absent are dropped.
func normaliseTurns(in []run.Turn) []run.Turn {
	out := make([]run.Turn, 0, len(in))
	for _, t := range in {
		switch t.Role {
		case "assistant":
			if strings.TrimSpace(t.Content) == "" && len(t.ToolCalls) == 0 {
				continue
			}
		case "tool":
			if t.ToolCallID == "" {
				continue
			}
		case "user":
			if n := len(out); n > 0 && out[n-1].Role == "user" {
				prev := &out[n-1]
				if prev.Content != "" && t.Content != "" {
					prev.Content += "\n\n" + t.Content
				} else {
					prev.Content += t.Content
				}
				prev.Attachments = t.Attachments
				prev.Directive = t.Directive
				continue
			}
		}
		out = append(out, t)
	}
	return out
}
