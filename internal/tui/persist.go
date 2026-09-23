package tui

import (
	"strings"

	"github.com/vulnetix/signet/internal/session"
	"github.com/vulnetix/signet/internal/tui/components"
)

// maxToolResultBytes caps a persisted tool result. Longer results are written
// truncated with meta.truncated and meta.orig_len so rehydrated history still
// knows what happened (and says so in the resume system line).
const maxToolResultBytes = 32 << 10

// rmText returns the plain-English line a rolemanager activity row renders:
// "Summary — Outcome". It is the persisted Content of a rolemanager entry.
func rmText(m components.Message) string {
	if m.RM.Summary == "" && m.RM.Outcome == "" {
		return ""
	}
	return m.RM.Summary + " — " + m.RM.Outcome
}

// neverPersisted reports whether a message can never produce a session entry.
// persistTail skips these rather than letting them block later messages.
func neverPersisted(m components.Message) bool {
	switch m.Role {
	case "assistant":
		// An assistant with neither text nor tool calls is never persisted,
		// matching buildTurns which skips it.
		return strings.TrimSpace(m.Text()) == "" && len(m.ToolCalls) == 0
	case "reasoning", "system":
		return strings.TrimSpace(m.Text()) == ""
	case "rolemanager":
		return strings.TrimSpace(rmText(m)) == ""
	default:
		return false
	}
}

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
		// A tool-calls assistant is final only once every one of its tool
		// results has landed too, so an assistant tool_calls entry can never
		// be written without its results following in the same file. The
		// trailing bubble is no exception: a turn aborted mid-tool must not
		// persist unpaired tool_calls.
		if len(m.ToolCalls) > 0 {
			return toolsAllSettled(msgs, i+1, len(m.ToolCalls))
		}
		if i != len(msgs)-1 {
			return true
		}
		// A trailing assistant bubble is in-flight while its text lives in the
		// streaming buffer (Content empty). EventDoneKind and EventErrorKind
		// call Materialise, which flushes buf into Content, so a non-empty
		// Content marks a turn that ended.
		return m.Content != ""
	case "tool":
		return m.Content != "" || m.Status != ""
	case "reasoning":
		// A reasoning bubble streams before the assistant turn. It is final
		// once a later message exists (the stream moved on to text or tools)
		// or once the turn ended and Materialise flushed its buffer.
		if i != len(msgs)-1 {
			return true
		}
		return m.Content != ""
	case "system", "rolemanager":
		// System notices and role-manager activity rows are appended whole,
		// never streamed, so they are final as soon as they exist.
		return true
	default:
		return false
	}
}

// toolsAllSettled reports whether the contiguous run of tool rows immediately
// following an assistant contains exactly want settled rows. A short run (an
// aborted turn) and a long run (an unexpected extra row) both fail, so an
// assistant tool_calls entry is never written without exactly its own results.
func toolsAllSettled(msgs []components.Message, start, want int) bool {
	n := 0
	for j := start; j < len(msgs); j++ {
		if msgs[j].Role != "tool" {
			break
		}
		if !settled(msgs, j) {
			return false
		}
		n++
	}
	return n == want
}

// persistTail appends entries for every settled message after the cursor and
// advances it, stopping at the first unsettled one. Idempotent.
func (a *App) persistTail() {
	for i := a.persistedUpTo; i < len(a.messages); i++ {
		m := a.messages[i]
		if neverPersisted(m) {
			// A trailing empty frame is the in-flight turn bubble (or a turn
			// that produced nothing) and must stop the scan; a non-trailing
			// one is a finalized no-op and is skipped so it cannot block later
			// settled messages.
			if i == len(a.messages)-1 {
				break
			}
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
		// Persist the per-message provider/model so restored history can show
		// which provider/model produced each turn even if settings later change.
		if m.Provider != "" {
			meta["provider"] = m.Provider
		}
		if m.Model != "" {
			meta["model"] = m.Model
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
		a.appendEntry(session.Entry{Type: "tool", Role: "tool", Content: content, Meta: meta, SubagentID: m.SubagentID})
	case "reasoning":
		meta := map[string]any{}
		if m.Provider != "" {
			meta["provider"] = m.Provider
		}
		if m.Model != "" {
			meta["model"] = m.Model
		}
		a.appendEntry(session.Entry{Type: "reasoning", Role: "reasoning", Content: m.Text(), Meta: meta})
	case "system":
		a.appendEntry(session.Entry{Type: "system", Role: "system", Content: m.Text()})
	case "rolemanager":
		meta := map[string]any{
			"summary": m.RM.Summary,
			"outcome": m.RM.Outcome,
			"tone":    int(m.RM.Tone),
			"level":   int(m.Level),
		}
		if m.Activity != "" {
			meta["activity"] = m.Activity
		}
		if m.Provider != "" {
			meta["provider"] = m.Provider
		}
		if m.Model != "" {
			meta["model"] = m.Model
		}
		a.appendEntry(session.Entry{
			Type:    "rolemanager",
			Role:    "rolemanager",
			Content: rmText(m),
			Meta:    meta,
		})
	}
}
