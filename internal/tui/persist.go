package tui

import (
	"strings"
	"unicode/utf8"

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
	case "reasoning", "system", completionRole, components.ReportRole:
		return strings.TrimSpace(m.Text()) == ""
	case "rolemanager":
		return strings.TrimSpace(rmText(m)) == ""
	default:
		return false
	}
}

// settled reports whether messages[i] is final: a tool row with no result, or
// the in-flight turn's assistant bubble, are not. Persisting only settled
// messages keeps the file structurally safe — an assistant tool_calls entry
// can never be written without its results following in the same file.
//
// inFlight is true while an agent turn is running. The TUI attaches every
// tool call of a turn to one assistant bubble, so the in-flight turn's latest
// assistant bubble may still gain calls and is held until the turn ends.
//
// force is the new-user-turn flush: whatever is still unsettled after a turn
// ended (an aborted call that never got its result) is written as far as it
// can be — the assistant with only its answered calls, unanswered tool rows
// dropped — instead of blocking every later row forever.
func settled(msgs []components.Message, i int, inFlight, force bool) bool {
	m := msgs[i]
	switch m.Role {
	case "user":
		return true
	case "assistant":
		if len(m.ToolCalls) > 0 {
			if force {
				return true
			}
			if inFlight && i == lastAssistant(msgs) {
				return false
			}
			// Results are matched by tool_call_id, not position: reasoning,
			// system and role-manager rows land between an assistant and its
			// tool rows in goal mode, and a positional count never matched,
			// so whole goal sessions were never persisted.
			return allCallsAnswered(msgs, i)
		}
		if i != len(msgs)-1 {
			return true
		}
		// A trailing assistant bubble is in-flight while its text lives in the
		// streaming buffer (Content empty). EventDoneKind and EventErrorKind
		// call Materialise, which flushes buf into Content, so a non-empty
		// Content marks a turn that ended.
		return m.Content != "" || force
	case "tool", components.ShellRole:
		return toolAnswered(m) || force
	case "reasoning":
		// A reasoning bubble streams before the assistant turn. It is final
		// once a later message exists (the stream moved on to text or tools)
		// or once the turn ended and Materialise flushed its buffer.
		if i != len(msgs)-1 {
			return true
		}
		return m.Content != "" || force
	case "system", "rolemanager", completionRole, components.ReportRole:
		// System notices, role-manager activity rows and the completion
		// panel are appended whole, never streamed, so they are final as soon
		// as they exist.
		return true
	default:
		return false
	}
}

// toolAnswered reports whether a tool row carries its result.
func toolAnswered(m components.Message) bool {
	return m.Content != "" || m.Status != ""
}

// lastAssistant returns the index of the last assistant message, or -1.
func lastAssistant(msgs []components.Message) int {
	for j := len(msgs) - 1; j >= 0; j-- {
		if msgs[j].Role == "assistant" {
			return j
		}
	}
	return -1
}

// answeredCallIDs returns the tool_call_ids that have an answered tool row
// after msgs[i]. Subagent rows are excluded: they belong to a subagent's own
// calls, never to this assistant.
func answeredCallIDs(msgs []components.Message, i int) map[string]bool {
	ids := map[string]bool{}
	for j := i + 1; j < len(msgs); j++ {
		t := msgs[j]
		if t.Role == "tool" && t.SubagentID == "" && t.ToolCallID != "" && toolAnswered(t) {
			ids[t.ToolCallID] = true
		}
	}
	return ids
}

// allCallsAnswered reports whether every tool call of msgs[i] has an answered
// tool row after it.
func allCallsAnswered(msgs []components.Message, i int) bool {
	ids := answeredCallIDs(msgs, i)
	for _, c := range msgs[i].ToolCalls {
		if !ids[c.ID] {
			return false
		}
	}
	return true
}

// persistTail appends entries for every settled message after the cursor and
// advances it, stopping at the first unsettled one. Idempotent.
func (a *App) persistTail() { a.persistTailMode(false) }

// persistTailMode is persistTail with the force flush described on settled.
// A turn still running is never forced: its rows keep arriving.
func (a *App) persistTailMode(force bool) {
	inFlight := a.cancel != nil
	force = force && !inFlight
	for i := a.persistedUpTo; i < len(a.messages); i++ {
		m := a.messages[i]
		if neverPersisted(m) {
			// A trailing empty frame is the in-flight turn bubble (or a turn
			// that produced nothing) and must stop the scan; a non-trailing
			// one is a finalized no-op and is skipped so it cannot block later
			// settled messages.
			if i == len(a.messages)-1 && !force {
				break
			}
			a.persistedUpTo = i + 1
			continue
		}
		if !settled(a.messages, i, inFlight, force) {
			break
		}
		if (m.Role == "tool" || m.Role == components.ShellRole) && !toolAnswered(m) {
			// Forced past an unanswered call: its assistant entry was written
			// without this call, so the row is dropped rather than orphaned.
			a.persistedUpTo = i + 1
			continue
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
		a.appendEntry(timedEntry(m, session.Entry{Type: "user", Role: "user", Content: m.Text()}))
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
			// Only answered calls are written, so a forced flush of an aborted
			// turn never leaves an unpaired tool_calls entry on disk.
			answered := answeredCallIDs(a.messages, i)
			calls := make([]map[string]any, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				if !answered[tc.ID] {
					continue
				}
				calls = append(calls, map[string]any{"id": tc.ID, "name": tc.Name, "args": tc.Args})
			}
			if len(calls) > 0 {
				meta["tool_calls"] = calls
			}
		}
		a.appendEntry(timedEntry(m, session.Entry{Type: "assistant", Role: "assistant", Content: m.Text(), Meta: meta}))
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
			content = truncateUTF8(content, maxToolResultBytes)
		}
		a.appendEntry(timedEntry(m, session.Entry{Type: "tool", Role: "tool", Content: content, Meta: meta, SubagentID: m.SubagentID}))
	case "reasoning":
		meta := map[string]any{}
		if m.Provider != "" {
			meta["provider"] = m.Provider
		}
		if m.Model != "" {
			meta["model"] = m.Model
		}
		a.appendEntry(timedEntry(m, session.Entry{Type: "reasoning", Role: "reasoning", Content: m.Text(), Meta: meta}))
	case "system":
		a.appendEntry(timedEntry(m, session.Entry{Type: "system", Role: "system", Content: m.Text(), SubagentID: m.SubagentID}))
	case completionRole:
		a.appendEntry(timedEntry(m, session.Entry{Type: completionRole, Role: completionRole, Content: m.Text()}))
	case components.ReportRole:
		a.appendEntry(timedEntry(m, session.Entry{Type: components.ReportRole, Role: components.ReportRole, Content: m.Text(), Meta: map[string]any{"title": m.ToolName, "meta": m.ToolArgs, "status": m.Status}}))
	case components.ShellRole:
		content := m.Text()
		meta := map[string]any{
			"command":  m.ShellCommand(),
			"shell_id": m.ToolCallID,
			"status":   m.Status,
		}
		if len(content) > maxToolResultBytes {
			meta["truncated"] = true
			meta["orig_len"] = len(content)
			content = truncateUTF8(content, maxToolResultBytes)
		}
		a.appendEntry(timedEntry(m, session.Entry{Type: components.ShellRole, Role: components.ShellRole, Content: content, Meta: meta}))
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
		a.appendEntry(timedEntry(m, session.Entry{
			Type:    "rolemanager",
			Role:    "rolemanager",
			Content: rmText(m),
			Meta:    meta,
		}))
	}
}

// truncateUTF8 cuts s to at most max bytes without splitting a multi-byte
// character, so a truncated tool result is still valid UTF-8 on disk.
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
