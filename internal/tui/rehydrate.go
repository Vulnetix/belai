package tui

import (
	"strings"

	"github.com/vulnetix/belai/internal/goals"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/todos"
	"github.com/vulnetix/belai/internal/transcript"
	"github.com/vulnetix/belai/internal/tui/components"
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

	// TextOnly: assistant entries but no tool entries, and no session_meta or
	// newer render-only rows (schema 1). A file carrying reasoning, system, or
	// rolemanager rows was written by a build that also persists tools, so a
	// text-only history there is genuine, not a schema-1 gap.
	hasAssistant, hasTool, hasMeta, hasNewRow := false, false, false, false
	for _, e := range entries {
		switch e.Type {
		case "assistant":
			hasAssistant = true
		case "tool":
			hasTool = true
		case session.EntryTypeSessionMeta:
			hasMeta = true
		case "reasoning", "system", "rolemanager", completionRole, components.ShellRole, components.ReportRole:
			hasNewRow = true
		}
	}
	r.TextOnly = hasAssistant && !hasTool && !hasMeta && !hasNewRow
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
			remoteID, _ := e.Meta["remote_prompt_id"].(string)
			msgs = append(msgs, components.Message{Role: "user", Content: e.Content, RemoteID: remoteID})
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
			msg := components.Message{
				Role:      "assistant",
				Content:   e.Content,
				ToolCalls: kept,
				Provider:  metaString(e.Meta, "provider"),
				Model:     metaString(e.Meta, "model"),
			}
			if u := usageFromMeta(e.Meta); u != nil {
				msg.Usage = u
			}
			msgs = append(msgs, msg)
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
				SubagentID: e.SubagentID,
			})
		case "reasoning":
			if strings.TrimSpace(e.Content) == "" {
				continue
			}
			msgs = append(msgs, components.Message{
				Role:     "reasoning",
				Content:  e.Content,
				Provider: metaString(e.Meta, "provider"),
				Model:    metaString(e.Meta, "model"),
			})
		case "system":
			if strings.TrimSpace(e.Content) == "" {
				continue
			}
			msgs = append(msgs, components.Message{Role: "system", Content: e.Content, SubagentID: e.SubagentID})
		case completionRole:
			if strings.TrimSpace(e.Content) == "" {
				continue
			}
			msgs = append(msgs, components.Message{Role: completionRole, Content: e.Content})
		case components.ReportRole:
			if strings.TrimSpace(e.Content) == "" {
				continue
			}
			msgs = append(msgs, components.Message{
				Role:     components.ReportRole,
				Content:  e.Content,
				ToolName: metaString(e.Meta, "title"),
				ToolArgs: metaString(e.Meta, "meta"),
				Status:   metaString(e.Meta, "status"),
			})
		case components.ShellRole:
			// A shell panel is render-only and pairs with nothing: the model's
			// copy of its output rides the following user turn's attachment.
			if e.Content == "" && metaString(e.Meta, "status") == "" {
				continue
			}
			msgs = append(msgs, components.Message{
				Role:       components.ShellRole,
				Content:    e.Content,
				ToolArgs:   components.ShellArgs(metaString(e.Meta, "command")),
				ToolCallID: metaString(e.Meta, "shell_id"),
				Status:     metaString(e.Meta, "status"),
			})
		case "rolemanager":
			if msg := rolemanagerMessage(e); msg.Role != "" {
				msgs = append(msgs, msg)
			}
		}
	}
	return msgs, dropped
}

// rolemanagerMessage rebuilds a rolemanager row from a persisted entry,
// restoring the rendered line and the structured description so a resumed
// transcript keeps both the text and the original tone/level gating. An entry
// with no renderable content returns an empty message and is dropped.
func rolemanagerMessage(e session.Entry) components.Message {
	content := strings.TrimSpace(e.Content)
	msg := components.Message{Role: "rolemanager", Content: content}
	if e.Meta != nil {
		if s, ok := e.Meta["summary"].(string); ok {
			msg.RM.Summary = s
		}
		if o, ok := e.Meta["outcome"].(string); ok {
			msg.RM.Outcome = o
		}
		if v, ok := metaInt(e.Meta, "tone"); ok {
			msg.RM.Tone = rolemanager.Tone(v)
		}
		if v, ok := metaInt(e.Meta, "level"); ok {
			msg.Level = rolemanager.Level(v)
		}
		msg.Activity = metaString(e.Meta, "activity")
		msg.Provider = metaString(e.Meta, "provider")
		msg.Model = metaString(e.Meta, "model")
	}
	if content == "" && msg.RM.Summary == "" && msg.RM.Outcome == "" {
		return components.Message{}
	}
	if msg.RM.Summary == "" && msg.RM.Outcome == "" {
		// Malformed entry without structured meta: keep the rendered line as a
		// plain system notice rather than a blank activity row.
		return components.Message{Role: "system", Content: content}
	}
	return msg
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

// usageFromMeta rebuilds a provider usage report from a persisted assistant
// entry's token meta, so the context estimate has an anchor immediately after
// rehydration.
func usageFromMeta(meta map[string]any) *transcript.Usage {
	if meta == nil {
		return nil
	}
	prompt, _ := metaInt(meta, "prompt_tokens")
	completion, _ := metaInt(meta, "completion_tokens")
	total, _ := metaInt(meta, "total_tokens")
	if prompt == 0 && completion == 0 && total == 0 {
		return nil
	}
	return &transcript.Usage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: total}
}

func metaInt(meta map[string]any, key string) (int, bool) {
	switch v := meta[key].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	}
	return 0, false
}
