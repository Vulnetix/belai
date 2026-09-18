package tui

import (
	"github.com/vulnetix/signet/internal/goals"
	"github.com/vulnetix/signet/internal/modes"
	"github.com/vulnetix/signet/internal/session"
	"github.com/vulnetix/signet/internal/todos"
	"github.com/vulnetix/signet/internal/tui/components"
)

// rehydrated is everything resumeSession restores from a session file.
type rehydrated struct {
	Messages []components.Message
	Name     string
	Summary  string
	Parent   string
	Todos    *todos.List
	Plan     *modes.PlanState
	Goal     *goals.GoalState
	Meta     session.Meta

	Model    string
	Provider string
	Effort   string
	Mode     string

	// TextOnly marks a schema-1 file (assistant entries but no tool entries):
	// tool activity from before tool persistence shipped cannot be restored.
	TextOnly bool
	// Dropped counts unpaired tool calls/results discarded by the pairing pass.
	Dropped int
}

// rehydrateSession decodes a session file into in-memory state. The state
// entries (summary, session_name, todo_list, plan_state, goal_state,
// session_meta) are consumed by their decoders and never become messages.
func rehydrateSession(entries []session.Entry) rehydrated {
	var r rehydrated
	r.Name = session.Name(entries)

	// A compaction summary anchors the fork: rehydrate only entries after it,
	// and buildTurns' synthetic summary pair reproduces the leading turns.
	summaryIdx := -1
	for i, e := range entries {
		if e.Type == "summary" {
			summaryIdx = i
		}
	}
	if summaryIdx >= 0 {
		r.Summary = entries[summaryIdx].Content
		if p, ok := entries[summaryIdx].Meta["parent_session"].(string); ok {
			r.Parent = p
		}
	}

	if list, ok := todos.Latest(entries); ok {
		r.Todos = &list
	}
	if ps, ok := modes.LatestPlanState(entries); ok {
		r.Plan = &ps
	}
	if gs, ok := goals.LatestGoalState(entries); ok {
		r.Goal = &gs
	}
	if meta, ok := session.LatestMeta(entries); ok {
		r.Meta = meta
	}

	r.Messages, r.Dropped = messagesFromEntries(entries)

	// Model/provider/effort come from the last assistant entry's meta; mode
	// prefers the session record and falls back to the same meta.
	var lastMeta map[string]any
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Type == "assistant" && entries[i].Meta != nil {
			lastMeta = entries[i].Meta
			break
		}
	}
	r.Model = metaString(lastMeta, "model")
	r.Provider = metaString(lastMeta, "provider")
	r.Effort = metaString(lastMeta, "effort")
	r.Mode = r.Meta.Mode
	if r.Mode == "" {
		r.Mode = metaString(lastMeta, "mode")
	}

	// TextOnly: assistant entries but no tool entries, and no session_meta
	// (schema 1).
	hasAssistant, hasTool, hasMeta := false, false, false
	for _, e := range entries {
		switch e.Type {
		case "assistant":
			hasAssistant = true
		case "tool":
			hasTool = true
		case session.EntryTypeSessionMeta:
			hasMeta = true
		}
	}
	r.TextOnly = hasAssistant && !hasTool && !hasMeta
	return r
}

// messagesFromEntries rebuilds the transcript, pairing assistant tool calls
// with their results. It is the safety-critical pairing pass: a call is kept
// only when a matching tool result exists at a later index, and a tool result
// is emitted only when a kept call referenced it. Orphans are dropped and
// counted.
func messagesFromEntries(entries []session.Entry) ([]components.Message, int) {
	// A summary entry anchors the fork: only entries after it rehydrate.
	start := 0
	for i, e := range entries {
		if e.Type == "summary" {
			start = i + 1
		}
	}
	emit := entries[start:]

	// Pass 1: index tool results by tool_call_id.
	results := map[string]int{}
	for i, e := range emit {
		if e.Type == "tool" {
			if id := metaString(e.Meta, "tool_call_id"); id != "" {
				results[id] = i
			}
		}
	}

	// Pass 2: emit, keeping only paired calls/results.
	var msgs []components.Message
	dropped := 0
	live := map[string]bool{}
	emitted := map[string]bool{}
	for i, e := range emit {
		switch e.Type {
		case "user":
			msgs = append(msgs, components.Message{Role: "user", Content: e.Content})
		case "assistant":
			var kept []components.AgentToolCall
			for _, c := range assistantToolCalls(e.Meta) {
				if idx, ok := results[c.ID]; ok && idx > i {
					kept = append(kept, c)
					live[c.ID] = true
				} else {
					dropped++
				}
			}
			// An assistant entry with empty text and zero calls is dropped.
			if e.Content == "" && len(kept) == 0 {
				continue
			}
			msgs = append(msgs, components.Message{Role: "assistant", Content: e.Content, ToolCalls: kept})
		case "tool":
			id := metaString(e.Meta, "tool_call_id")
			if id == "" || !live[id] {
				dropped++
				continue
			}
			if emitted[id] {
				continue
			}
			emitted[id] = true
			msgs = append(msgs, components.Message{
				Role:       "tool",
				Content:    e.Content,
				ToolName:   metaString(e.Meta, "tool_name"),
				ToolArgs:   metaString(e.Meta, "tool_args"),
				Status:     metaString(e.Meta, "status"),
				ToolCallID: id,
			})
		}
	}
	return msgs, dropped
}

// assistantToolCalls decodes the persisted tool_calls meta of an assistant
// entry. Malformed call records are skipped rather than fatal.
func assistantToolCalls(meta map[string]any) []components.AgentToolCall {
	raw, ok := meta["tool_calls"].([]any)
	if !ok {
		return nil
	}
	var out []components.AgentToolCall
	for _, r := range raw {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["id"].(string)
		if id == "" {
			continue
		}
		name, _ := m["name"].(string)
		args, _ := m["args"].(string)
		out = append(out, components.AgentToolCall{ID: id, Name: name, Args: args})
	}
	return out
}

// metaString reads a string from an entry meta map, or "".
func metaString(meta map[string]any, key string) string {
	if meta == nil {
		return ""
	}
	if v, ok := meta[key].(string); ok {
		return v
	}
	return ""
}
