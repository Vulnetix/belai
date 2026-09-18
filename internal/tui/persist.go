package tui

import (
	"github.com/vulnetix/signet/internal/session"
	"github.com/vulnetix/signet/internal/tui/components"
)

// maxToolResultBytes caps a persisted tool result. Longer results are written
// truncated with meta.truncated and meta.orig_len so rehydrated history still
// knows what happened (and says so in the resume system line).
const maxToolResultBytes = 32 << 10

// settled reports whether messages[i] is final: a tool row with no result, or
// the in-flight trailing assistant bubble, are not. Persisting only settled
// messages keeps the file structurally safe — an assistant tool_calls entry
// can never be written without its results following in the same file.
func settled(msgs []components.Message, i int) bool {
	m := msgs[i]
	switch m.Role {
	case "user":
		return true
	case "assistant":
		// An assistant with neither text nor tool calls is never persisted,
		// matching buildTurns which skips it.
		if m.Content == "" && len(m.ToolCalls) == 0 {
			return false
		}
		if i != len(msgs)-1 {
			// A tool-calls assistant is final only once every one of its tool
			// results has landed too, so an assistant tool_calls entry can
			// never be written without its results following in the same file.
			if len(m.ToolCalls) > 0 {
				return toolsAllSettled(msgs, i+1)
			}
			return true
		}
		// A trailing assistant bubble is in-flight while its text lives in the
		// streaming buffer (Content empty). EventDoneKind calls Materialise,
		// which flushes buf into Content, so a non-empty Content marks a turn
		// that ended.
		return m.Content != ""
	case "tool":
		return m.Content != "" || m.Status != ""
	default:
		// reasoning and system never persist, matching buildTurns.
		return false
	}
}

// toolsAllSettled reports whether every tool row immediately following an
// assistant (the contiguous run of tool messages) has its result landed.
func toolsAllSettled(msgs []components.Message, start int) bool {
	for j := start; j < len(msgs); j++ {
		if msgs[j].Role != "tool" {
			break
		}
		if !settled(msgs, j) {
			return false
		}
	}
	return true
}

// persistTail appends entries for every settled message after the cursor and
// advances it, stopping at the first unsettled one. Idempotent.
func (a *App) persistTail() {
	for i := a.persistedUpTo; i < len(a.messages); i++ {
		m := a.messages[i]
		// reasoning and system are skipped entirely, matching buildTurns.
		// Advancing the cursor past them means a settled assistant after a
		// pass-loop verdict is still written.
		if m.Role == "reasoning" || m.Role == "system" {
			a.persistedUpTo = i + 1
			continue
		}
		if !settled(a.messages, i) {
			break
		}
		a.persistMessage(i)
		a.persistedUpTo = i + 1
	}
}

// persistMessage writes one settled message as a session entry. User turns are
// normally written by echoUser (which advances the cursor); the case remains
// for safety so a rehydrated or resumed cursor never double-writes by surprise.
func (a *App) persistMessage(i int) {
	m := a.messages[i]
	switch m.Role {
	case "user":
		a.appendEntry(session.Entry{Type: "user", Role: "user", Content: m.Text()})
	case "assistant":
		meta := map[string]any{
			"model":    a.cfg.Model,
			"provider": a.cfg.Provider,
			"mode":     a.mode,
			"effort":   a.cfg.Effort,
		}
		if m.Usage != nil {
			meta["prompt_tokens"] = m.Usage.PromptTokens
			meta["completion_tokens"] = m.Usage.CompletionTokens
			meta["total_tokens"] = m.Usage.Total()
		}
		if len(m.ToolCalls) > 0 {
			calls := make([]map[string]any, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				calls = append(calls, map[string]any{"id": tc.ID, "name": tc.Name, "args": tc.Args})
			}
			meta["tool_calls"] = calls
		}
		a.appendEntry(session.Entry{Type: "assistant", Role: "assistant", Content: m.Text(), Meta: meta})
	case "tool":
		content := m.Text()
		meta := map[string]any{
			"tool_call_id": m.ToolCallID,
			"tool_name":    m.ToolName,
			"tool_args":    m.ToolArgs,
			"status":       m.Status,
		}
		if len(content) > maxToolResultBytes {
			meta["truncated"] = true
			meta["orig_len"] = len(content)
			content = content[:maxToolResultBytes]
		}
		a.appendEntry(session.Entry{Type: "tool", Role: "tool", Content: content, Meta: meta})
	}
}
