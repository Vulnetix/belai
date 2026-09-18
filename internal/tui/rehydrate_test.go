package tui

import (
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/goals"
	"github.com/vulnetix/signet/internal/modes"
	"github.com/vulnetix/signet/internal/session"
	"github.com/vulnetix/signet/internal/todos"
	"github.com/vulnetix/signet/internal/tui/components"
)

// assertPairing asserts the shared post-conditions of a rehydrated transcript:
// every assistant call id has exactly one later matching tool message; every
// tool message id appears in exactly one earlier assistant's calls; no message
// has both empty text and zero calls.
func assertPairing(t *testing.T, msgs []components.Message) {
	t.Helper()
	callToTools := map[string]int{}
	toolToCalls := map[string]int{}
	for _, m := range msgs {
		if strings.TrimSpace(m.Text()) == "" && len(m.ToolCalls) == 0 && m.Role != "tool" {
			t.Fatalf("empty non-tool message: %+v", m)
		}
		for _, tc := range m.ToolCalls {
			callToTools[tc.ID]++
		}
		if m.Role == "tool" {
			toolToCalls[m.ToolCallID]++
		}
	}
	for id := range callToTools {
		if toolToCalls[id] != 1 {
			t.Fatalf("assistant call %q has %d tool messages, want 1", id, toolToCalls[id])
		}
	}
	for id := range toolToCalls {
		if callToTools[id] != 1 {
			t.Fatalf("tool message %q has %d assistant calls, want 1", id, callToTools[id])
		}
	}
}

func TestRehydrateTextOnlySession(t *testing.T) {
	entries := []session.Entry{
		{ID: "u1", Type: "user", Role: "user", Content: "hello"},
		{ID: "a1", ParentID: "u1", Type: "assistant", Role: "assistant", Content: "hi", Meta: map[string]any{"model": "m", "provider": "p"}},
		{ID: "u2", ParentID: "a1", Type: "user", Role: "user", Content: "again"},
		{ID: "a2", ParentID: "u2", Type: "assistant", Role: "assistant", Content: "sure"},
	}
	r := rehydrateSession(entries)
	if len(r.Messages) != 4 {
		t.Fatalf("messages = %d, want 4", len(r.Messages))
	}
	if !r.TextOnly {
		t.Fatal("TextOnly = false, want true for schema-1 text session")
	}
	if r.Model != "m" || r.Provider != "p" {
		t.Fatalf("model/provider = %q/%q", r.Model, r.Provider)
	}
	assertPairing(t, r.Messages)
}

func TestRehydrateToolPair(t *testing.T) {
	entries := []session.Entry{
		{ID: "u1", Type: "user", Role: "user", Content: "go"},
		{ID: "a1", ParentID: "u1", Type: "assistant", Role: "assistant", Content: "", Meta: map[string]any{"tool_calls": []any{map[string]any{"id": "c1", "name": "Bash", "args": `{"command":"ls"}`}}}},
		{ID: "t1", ParentID: "a1", Type: "tool", Role: "tool", Content: "result", Meta: map[string]any{"tool_call_id": "c1", "tool_name": "Bash", "tool_args": `{"command":"ls"}`, "status": "✓"}},
	}
	r := rehydrateSession(entries)
	if r.TextOnly {
		t.Fatal("TextOnly = true, want false for a tool session")
	}
	if len(r.Messages) != 3 {
		t.Fatalf("messages = %d, want 3: %+v", len(r.Messages), r.Messages)
	}
	if len(r.Messages[1].ToolCalls) != 1 || r.Messages[1].ToolCalls[0].ID != "c1" {
		t.Fatalf("assistant calls = %+v", r.Messages[1].ToolCalls)
	}
	if r.Messages[2].ToolCallID != "c1" || r.Messages[2].Content != "result" {
		t.Fatalf("tool message = %+v", r.Messages[2])
	}
	assertPairing(t, r.Messages)
}

func TestRehydrateDropsOrphanCallAndResult(t *testing.T) {
	entries := []session.Entry{
		{ID: "u1", Type: "user", Role: "user", Content: "go"},
		{ID: "a1", ParentID: "u1", Type: "assistant", Role: "assistant", Content: "", Meta: map[string]any{"tool_calls": []any{
			map[string]any{"id": "good", "name": "Bash", "args": "{}"},
			map[string]any{"id": "orphan-call", "name": "Bash", "args": "{}"},
		}}},
		{ID: "t1", ParentID: "a1", Type: "tool", Role: "tool", Content: "good result", Meta: map[string]any{"tool_call_id": "good"}},
		{ID: "t2", ParentID: "a1", Type: "tool", Role: "tool", Content: "orphan result", Meta: map[string]any{"tool_call_id": "orphan-result"}},
	}
	r := rehydrateSession(entries)
	if len(r.Messages) != 3 {
		t.Fatalf("messages = %d, want 3: %+v", len(r.Messages), r.Messages)
	}
	if r.Dropped != 2 {
		t.Fatalf("Dropped = %d, want 2", r.Dropped)
	}
	if len(r.Messages[1].ToolCalls) != 1 || r.Messages[1].ToolCalls[0].ID != "good" {
		t.Fatalf("kept calls = %+v", r.Messages[1].ToolCalls)
	}
	if r.Messages[2].ToolCallID != "good" {
		t.Fatalf("kept tool = %+v", r.Messages[2])
	}
	assertPairing(t, r.Messages)
}

func TestRehydrateDropsEmptyAssistant(t *testing.T) {
	entries := []session.Entry{
		{ID: "u1", Type: "user", Role: "user", Content: "go"},
		{ID: "a1", ParentID: "u1", Type: "assistant", Role: "assistant", Content: "", Meta: map[string]any{}},
		{ID: "u2", ParentID: "a1", Type: "user", Role: "user", Content: "next"},
	}
	r := rehydrateSession(entries)
	if len(r.Messages) != 2 {
		t.Fatalf("messages = %d, want 2 (empty assistant dropped): %+v", len(r.Messages), r.Messages)
	}
	assertPairing(t, r.Messages)
}

func TestRehydrateRestoresState(t *testing.T) {
	plan := modes.PlanState{Enabled: true, Todos: []modes.Todo{{N: 1, Text: "step"}}}
	goal := goals.GoalState{Version: 1, Objective: "finish it", Status: string(goals.StatusActive)}
	list := todos.New("prompt", []string{"a", "b"})

	entries := []session.Entry{
		{ID: "m1", Type: session.EntryTypeSessionMeta, Content: `{"schema":2,"cwd":"/proj","mode":"plan","activePlan":"p1"}`},
		{ID: "n1", Type: session.EntryTypeSessionName, Content: "named session"},
		{ID: "s1", Type: "summary", Content: "summarised", Meta: map[string]any{"parent_session": "old-id"}},
		{ID: "u1", ParentID: "s1", Type: "user", Role: "user", Content: "after summary"},
		{ID: "a1", ParentID: "u1", Type: "assistant", Role: "assistant", Content: "ok"},
		plan.ToEntry("a1"),
		goal.ToEntry("a1"),
		list.ToEntry("a1"),
	}

	r := rehydrateSession(entries)
	if r.Name != "named session" {
		t.Fatalf("Name = %q", r.Name)
	}
	if r.Summary != "summarised" || r.Parent != "old-id" {
		t.Fatalf("summary/parent = %q/%q", r.Summary, r.Parent)
	}
	if r.Plan == nil || !r.Plan.Enabled || len(r.Plan.Todos) != 1 {
		t.Fatalf("Plan = %+v", r.Plan)
	}
	if r.Goal == nil || r.Goal.Objective != "finish it" {
		t.Fatalf("Goal = %+v", r.Goal)
	}
	if r.Todos == nil || len(r.Todos.Items) != 2 {
		t.Fatalf("Todos = %+v", r.Todos)
	}
	if r.Meta.Mode != "plan" || r.Meta.ActivePlan != "p1" {
		t.Fatalf("Meta = %+v", r.Meta)
	}
	if r.Mode != "plan" {
		t.Fatalf("Mode = %q", r.Mode)
	}
	// Summary anchors the fork: only entries after it rehydrate as messages.
	if len(r.Messages) != 2 {
		t.Fatalf("messages = %d, want 2: %+v", len(r.Messages), r.Messages)
	}
}

func TestRehydrateSkipsMalformedPlanState(t *testing.T) {
	entries := []session.Entry{
		{ID: "bad", Type: "plan_state", Content: "{not json"},
		{ID: "good", Type: "plan_state", Content: `{"enabled":true,"todos":[{"n":1,"text":"x"}]}`},
	}
	ps, ok := modes.LatestPlanState(entries)
	if !ok {
		t.Fatal("LatestPlanState not found")
	}
	if !ps.Enabled || len(ps.Todos) != 1 {
		t.Fatalf("PlanState = %+v", ps)
	}
}
