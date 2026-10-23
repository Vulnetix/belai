package components

import (
	"fmt"
	"strings"
)

// A2ARole is the transcript role of an A2A row (docs/a2a.md): a broadcast
// sent or received, a direct message, a reply, an acknowledgement or a relay
// state change. It is render-only: buildTurns never promotes it, so no model
// reads it through the transcript; what a model is told of A2A rides on a
// directive or a classified attachment. The entry type (a2a_message_in, …)
// rides on ToolName and the linkage facts (messageId, peer, repo) on Meta.
const A2ARole = "a2a"

// A2A entry types, as the session file records them.
const (
	A2ABroadcastOut = "a2a_broadcast_out"
	A2ABroadcastIn  = "a2a_broadcast_in"
	A2AMessageOut   = "a2a_message_out"
	A2AMessageIn    = "a2a_message_in"
	A2AReplyOut     = "a2a_reply_out"
	A2AReplyIn      = "a2a_reply_in"
	A2AAck          = "a2a_ack"
	A2AState        = "a2a_state"
)

// A2AEntryTypes lists the A2A entry types.
var A2AEntryTypes = []string{A2ABroadcastOut, A2ABroadcastIn, A2AMessageOut, A2AMessageIn, A2AReplyOut, A2AReplyIn, A2AAck, A2AState}

// IsA2AEntry reports whether an entry type is an A2A row's.
func IsA2AEntry(t string) bool {
	for _, x := range A2AEntryTypes {
		if x == t {
			return true
		}
	}
	return false
}

// a2aGlyph is the row's direction glyph: ⇄ broadcast, → message out,
// ← message in, ↩ reply, · ack and state.
func a2aGlyph(kind string) string {
	switch kind {
	case A2ABroadcastOut, A2ABroadcastIn:
		return "⇄"
	case A2AMessageOut:
		return "→"
	case A2AMessageIn:
		return "←"
	case A2AReplyOut, A2AReplyIn:
		return "↩"
	}
	return "·"
}

func a2aTitle(kind string) string {
	switch kind {
	case A2ABroadcastOut:
		return "broadcast"
	case A2ABroadcastIn:
		return "broadcast from"
	case A2AMessageOut:
		return "message to"
	case A2AMessageIn:
		return "message from"
	case A2AReplyOut:
		return "reply to"
	case A2AReplyIn:
		return "reply from"
	case A2AAck:
		return "ack"
	case A2AState:
		return "A2A"
	}
	return "A2A"
}

func metaStr(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	s, _ := m[k].(string)
	return s
}

// a2aPanel renders an A2A row: the glyph and kind, the peer's display id,
// the work it is about (repo@branch, PR, ticket), and the text (a message or
// reply's parts, a broadcast's files, an ack's reason).
func a2aPanel(msg Message, width int, expandAll bool) (string, LineMap) {
	kind := msg.ToolName
	title := a2aGlyph(kind) + " " + a2aTitle(kind)
	if peer := metaStr(msg.Meta, "peerDisplay"); peer != "" && kind != A2ABroadcastOut && kind != A2AState {
		title += " " + peer
	}
	var meta []string
	if repo := metaStr(msg.Meta, "repo"); repo != "" {
		if br := metaStr(msg.Meta, "branch"); br != "" {
			repo += "@" + br
		}
		meta = append(meta, repo)
	}
	if pr := msg.Meta["pr"]; pr != nil {
		switch v := pr.(type) {
		case int:
			if v > 0 {
				meta = append(meta, fmt.Sprintf("PR #%d", v))
			}
		case float64:
			if v > 0 {
				meta = append(meta, fmt.Sprintf("PR #%d", int(v)))
			}
		}
	}
	if t := metaStr(msg.Meta, "ticket"); t != "" {
		meta = append(meta, t)
	}
	body := strings.TrimRight(msg.Text(), "\n")
	if ref := metaStr(msg.Meta, "referenceMessageId"); ref != "" && (kind == A2AReplyIn || kind == A2AReplyOut || kind == A2AAck) {
		short := ref
		if len(short) > 8 {
			short = short[:8]
		}
		body = "re " + short + ": " + body
	}
	inner := max(width-4, 8)
	md := RenderMarkdown(body, inner)
	rows := md.Rows
	if !expandAll && !msg.Expanded {
		rows, _, _ = truncateMarkdown(body, md, reportPreviewLines)
	}
	p := Panel{Title: title, Meta: strings.Join(meta, " · "), Width: width, Accent: ColorA2A, BodyRows: rows, Open: true}
	return p.Render()
}
