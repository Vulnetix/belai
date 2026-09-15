package transcript

import (
	"fmt"
	"strings"
)

// DefaultMaxToolResultChars bounds a single tool result inside a serialized
// conversation.
const DefaultMaxToolResultChars = 2000

// SerializeOptions configures Serialize.
type SerializeOptions struct {
	MaxToolResultChars int    // 0 means DefaultMaxToolResultChars
	Nonce              string // tag id suffix; empty means an unsuffixed tag
}

// Serialize flattens a conversation into a single plain-text block wrapped in
// <conversation> tags. Rendering the conversation as content rather than
// replaying the message array is what makes the compaction call safe: the
// model is reading a document, not being handed a dialogue to continue.
// System-role harness notices are dropped — they are UI chrome, not
// conversation. Tool results are truncated rune-safely with an explicit
// marker so the model knows material was elided.
func Serialize(msgs []Message, opts SerializeOptions) string {
	max := opts.MaxToolResultChars
	if max <= 0 {
		max = DefaultMaxToolResultChars
	}

	openTag := "<conversation"
	if opts.Nonce != "" {
		openTag += ` id="` + opts.Nonce + `"`
	}
	openTag += ">"

	var b strings.Builder
	b.WriteString(openTag)
	b.WriteString("\n")
	for _, m := range msgs {
		if m.Role == "system" {
			continue
		}
		label := "[" + m.Role + "]"
		if m.Role == "tool" && m.ToolName != "" {
			label = "[tool: " + m.ToolName + "]"
		}
		content := m.Content
		if m.Role == "tool" {
			content = truncateRunes(content, max)
		}
		b.WriteString(label)
		b.WriteString("\n")
		b.WriteString(content)
		b.WriteString("\n\n")
	}
	b.WriteString("</conversation>")
	return b.String()
}

// truncateRunes truncates s at a rune boundary, appending an explicit marker
// naming the total rune count so elision is visible.
func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	cut := string(runes[:max])
	return cut + fmt.Sprintf("… (truncated, %d chars total)", len(runes))
}
