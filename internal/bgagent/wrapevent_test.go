package bgagent

import (
	"testing"

	"github.com/vulnetix/signet/internal/agent"
	"github.com/vulnetix/signet/internal/rolemanager"
)

// A tool start carries its call in Tool; the wrapped event lifts the name, id
// and arguments so the TUI can open the row and settle it on the result.
func TestWrapEventLiftsTheToolCall(t *testing.T) {
	m := &Manager{}
	e := m.wrapEvent("triage", agent.Event{
		Kind: agent.EventToolStartKind,
		Tool: &rolemanager.ToolCall{ID: "c1", Name: "Read", Args: map[string]any{"path": "go.sum"}},
	})
	if e.ToolName != "Read" || e.ToolCallID != "c1" || e.ToolArgs != `{"path":"go.sum"}` {
		t.Fatalf("wrapped = %+v", e)
	}

	raw := m.wrapEvent("triage", agent.Event{
		Kind: agent.EventToolStartKind,
		Tool: &rolemanager.ToolCall{ID: "c2", Name: "Grep", RawArgs: `{"pattern":"x"`},
	})
	if raw.ToolArgs != `{"pattern":"x"` {
		t.Fatalf("raw args not kept: %q", raw.ToolArgs)
	}

	res := m.wrapEvent("triage", agent.Event{Kind: agent.EventToolResultKind, ToolName: "Read", ToolResult: "ok", ToolCallID: "c1"})
	if res.ToolCallID != "c1" || res.ToolResult != "ok" {
		t.Fatalf("result = %+v", res)
	}
}
