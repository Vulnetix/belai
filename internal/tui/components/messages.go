// Package components holds the Bubble Tea building blocks for the Signet TUI.
package components

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/vulnetix/signet/internal/transcript"
)

// AgentToolCall records a tool call that belongs to an assistant turn. The
// raw JSON arguments are kept as text so this package stays UI-only; the
// caller parses JSON when rebuilding provider turns.
type AgentToolCall struct {
	ID   string
	Name string
	Args string // raw JSON text
}

// Message is one message in the transcript.
type Message struct {
	Role     string // user, assistant, tool, system
	Content  string
	Usage    *transcript.Usage // non-nil on metered assistant turns
	ToolName string            // set on tool turns
	ToolArgs string            // set on tool turns
	Status   string            // set on tool turns (✓, withheld, …)

	// Expanded overrides global truncation for this message.
	Expanded bool

	// Partial is set on assistant bubbles that belong to a turn that failed
	// and is being retried. They are dimmed and skipped when rebuilding the
	// provider-facing transcript.
	Partial bool

	// ToolCallID is set on tool turns. It groups a result turn back to the
	// assistant call that requested it.
	ToolCallID string

	// ToolCalls records the calls requested by an assistant turn. It is only
	// meaningful when Role == "assistant"; it lets buildTurns preserve the
	// tool-call metadata across rounds.
	ToolCalls []AgentToolCall

	// Steering marks a user turn injected mid-loop while the agent is running.
	Steering bool
}

// MessageList renders the transcript.
type MessageList struct {
	Messages  []Message
	Width     int
	ExpandAll bool // when true, render every message in full

	// ShowReasoning and ShowTools gate the dim reasoning panel and tool rows,
	// mirroring the ctrl+r / ctrl+t toggles resolved by the caller.
	ShowReasoning bool
	ShowTools     bool
}

const (
	messageMinWidth       = 32
	assistantPreviewLines = 4
	toolPreviewLines      = 1
)

// View renders the transcript: conversational turns as flat titled panels,
// tool calls and system notices as single-line rows between them. Empty
// assistant/user frames with no tool calls are skipped so a tool-calls-only
// turn never renders a bare box. Framed panels are separated by a blank line;
// consecutive flat rows sit on adjacent lines.
func (m MessageList) View() string {
	width := max(m.Width, messageMinWidth)

	type entry struct {
		msg    Message
		framed bool
	}
	var entries []entry
	for _, msg := range m.Messages {
		switch msg.Role {
		case "reasoning":
			if !m.ShowReasoning {
				continue
			}
			entries = append(entries, entry{msg, true})
		case "tool":
			if !m.ShowTools {
				continue
			}
			entries = append(entries, entry{msg, false})
		case "system":
			entries = append(entries, entry{msg, false})
		default:
			if strings.TrimSpace(msg.Content) == "" && len(msg.ToolCalls) == 0 {
				continue
			}
			entries = append(entries, entry{msg, true})
		}
	}

	var b strings.Builder
	for i, e := range entries {
		switch e.msg.Role {
		case "tool":
			b.WriteString(toolRow(e.msg, width, m.ExpandAll))
		case "system":
			b.WriteString(systemRow(e.msg.Content, width))
		case "reasoning":
			b.WriteString(reasoningPanel(e.msg, width, m.ExpandAll))
		default:
			b.WriteString(turnPanel(e.msg, width, m.ExpandAll))
		}
		if i == len(entries)-1 {
			break
		}
		if e.framed || entries[i+1].framed {
			b.WriteString("\n\n")
		} else {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// reasoningPanel renders streamed chain-of-thought as a dim, unbordered
// sibling of the assistant panel, truncated like any other turn.
func reasoningPanel(msg Message, width int, expandAll bool) string {
	body := strings.TrimRight(msg.Content, "\n")
	if !expandAll && !msg.Expanded {
		body = truncateBody(body, assistantPreviewLines)
	}
	body = MutedStyle.Render(body)
	return Panel{
		Title:  "reasoning",
		Body:   body,
		Width:  width,
		Accent: lipgloss.TerminalColor(ColorMuted),
	}.View()
}

// turnPanel renders a user or assistant turn. When the turn is longer than
// assistantPreviewLines and the transcript is not expanded, only the first
// few lines are shown with a trailing count of hidden lines.
func turnPanel(msg Message, width int, expandAll bool) string {
	title, accent := "signet", lipgloss.TerminalColor(ColorTeal)
	if msg.Role == "user" {
		title, accent = "user prompt", lipgloss.TerminalColor(ColorTealSoft)
		if msg.Steering {
			title, accent = "user steering", lipgloss.TerminalColor(ColorAmber)
		}
	} else if msg.Role != "assistant" {
		title, accent = msg.Role, lipgloss.TerminalColor(ColorMuted)
	}

	meta := ""
	if msg.Usage != nil {
		if n := msg.Usage.Total(); n > 0 {
			meta = formatTokens(n) + " tok"
		}
	}
	if msg.Partial {
		meta = "retrying…"
	}

	body := strings.TrimRight(msg.Content, "\n")
	if strings.TrimSpace(body) == "" && len(msg.ToolCalls) > 0 {
		body = toolCallSummary(msg.ToolCalls, width)
	}
	if !expandAll && !msg.Expanded {
		body = truncateBody(body, assistantPreviewLines)
	}
	if msg.Partial {
		body = MutedStyle.Render(body)
		title = MutedStyle.Render(title)
	}

	return Panel{
		Title:  title,
		Meta:   meta,
		Body:   body,
		Width:  width,
		Accent: accent,
	}.View()
}

// toolCallSummary renders a muted one-line substitute for an assistant turn
// whose only output was tool calls. Tool names are deduped, order preserved,
// and the line is truncated to the panel's inner width.
func toolCallSummary(calls []AgentToolCall, width int) string {
	seen := map[string]bool{}
	var names []string
	for _, c := range calls {
		if c.Name == "" || seen[c.Name] {
			continue
		}
		seen[c.Name] = true
		names = append(names, c.Name)
	}
	label := "requested " + strconv.Itoa(len(names)) + " tools"
	if len(names) == 0 {
		label = "requested tools"
	}
	line := label + " · " + strings.Join(names, ", ")
	inner := max(width-4, 8)
	return MutedStyle.Render(truncateRunes(line, inner))
}

// truncateBody keeps up to maxLines of body and appends a muted hint when
// content was hidden.
func truncateBody(body string, maxLines int) string {
	if maxLines < 1 {
		return body
	}
	lines := strings.Split(body, "\n")
	if len(lines) <= maxLines {
		return body
	}
	kept := strings.Join(lines[:maxLines], "\n")
	hidden := len(lines) - maxLines
	hint := MutedStyle.Render("… " + strconv.Itoa(hidden) + " more lines")
	return kept + "\n" + hint
}

// toolRow renders one tool call as a flat row — tool activity is subordinate
// to the turn that caused it, so it never gets a frame of its own. The first
// line shows the tool name, its human-readable invocation, and the status.
// When the result is available, a preview of the first line of stdout/stderr
// is shown beneath; bash errors are rendered in red.
func toolRow(msg Message, width int, expandAll bool) string {
	isErr := toolResultIsError(msg.ToolName, msg.Content)

	status := strings.TrimSpace(msg.Status)
	if status == "" {
		switch {
		case isErr:
			status = "✗"
		case strings.HasPrefix(msg.Content, "tool result withheld:"):
			status = "withheld"
		default:
			status = "✓"
		}
	}

	head := MutedStyle.Render("⌁ " + msg.ToolName)
	plain := "⌁ " + msg.ToolName

	if invocation := formatToolInvocation(msg.ToolName, msg.ToolArgs); invocation != "" {
		head += "  " + MutedStyle.Render(invocation)
		plain += "  " + invocation
	}

	statusLine := alignStatus(head, plain, status, width)

	content := strings.TrimRight(msg.Content, "\n")
	if content == "" {
		return statusLine
	}

	expand := expandAll || msg.Expanded
	preview := content
	if !expand {
		lines := strings.Split(content, "\n")
		preview = lines[0]
		if len(lines) > toolPreviewLines {
			hidden := len(lines) - toolPreviewLines
			preview += "  " + MutedStyle.Render("… "+strconv.Itoa(hidden)+" more lines")
		}
	}
	return statusLine + "\n" + renderToolContent(preview, width, isErr)
}

// alignStatus right-aligns the status on the same line as the tool header,
// truncating the header if necessary.
func alignStatus(head, plain, status string, width int) string {
	if status == "" {
		return truncateLine(head, plain, width)
	}

	pad := width - visibleLen(plain) - visibleLen(status)
	if pad < 1 {
		trimTo := width - visibleLen(status) - 2
		head = truncateLine(head, plain, trimTo)
		plain = truncateRunes(plain, max(trimTo, 1))
		pad = max(width-visibleLen(plain)-visibleLen(status), 1)
	}
	return head + spaces(pad) + statusStyle(status).Render(status)
}

// renderToolContent indents and wraps a tool result line.
func renderToolContent(content string, width int, isErr bool) string {
	prefix := "  "
	inner := max(width-visibleLen(prefix), 8)
	body := content
	if isErr {
		body = DangerStyle.Render(body)
	}
	rendered := lipgloss.NewStyle().Width(inner).Render(body)
	var b strings.Builder
	first := true
	for _, line := range strings.Split(rendered, "\n") {
		if !first {
			b.WriteString("\n")
		}
		first = false
		b.WriteString(prefix + strings.TrimRight(line, " "))
	}
	return b.String()
}

// formatToolInvocation extracts the most descriptive argument from a tool's
// JSON args for display next to the tool name.
func formatToolInvocation(name, argsJSON string) string {
	argsJSON = strings.TrimSpace(argsJSON)
	if argsJSON == "" {
		return ""
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		s := strings.ReplaceAll(argsJSON, "\n", " ")
		return truncateRunes(s, 60)
	}

	keyOrder := map[string][]string{
		"Bash":      {"command", "cmd"},
		"Read":      {"path", "file"},
		"Grep":      {"pattern", "query"},
		"Glob":      {"pattern", "query"},
		"WebSearch": {"query", "q"},
		"WebFetch":  {"url"},
	}

	keys := keyOrder[name]
	if len(keys) == 0 {
		keys = []string{"command", "path", "pattern", "query", "url", "args"}
	}
	for _, k := range keys {
		if v, ok := args[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return truncateRunes(s, 120)
			}
		}
	}
	for _, v := range args {
		if s, ok := v.(string); ok && s != "" {
			return truncateRunes(s, 120)
		}
	}
	return ""
}

// toolResultIsError reports whether a tool result represents a failure that
// should be highlighted in red. Bash non-zero exits are detected by the
// "exit status" marker in their output; all "tool result withheld" strings
// indicate the tool did not return useful data.
func toolResultIsError(name, content string) bool {
	if strings.HasPrefix(content, "tool result withheld:") {
		return true
	}
	if name == "Bash" && strings.Contains(content, "exit status") {
		return true
	}
	return false
}

// systemRow renders a system notice as a dim, marked line.
func systemRow(content string, width int) string {
	body := strings.TrimRight(content, "\n")
	marker := MutedStyle.Render("│ ")
	wrapped := lipgloss.NewStyle().Foreground(ColorMuted).Width(max(width-2, 8)).Render(body)

	var b strings.Builder
	first := true
	for _, line := range strings.Split(wrapped, "\n") {
		if !first {
			b.WriteString("\n")
		}
		first = false
		b.WriteString(marker + strings.TrimRight(line, " "))
	}
	return b.String()
}

func statusStyle(status string) lipgloss.Style {
	switch {
	case strings.Contains(status, "✓"), strings.Contains(status, "ok"):
		return AccentStyle
	case strings.Contains(status, "✗"), strings.Contains(status, "denied"),
		strings.Contains(status, "error"), strings.Contains(status, "withheld"):
		return DangerStyle
	default:
		return MutedStyle
	}
}

// truncateLine clips a styled line whose plain-text twin is `plain`.
func truncateLine(styled, plain string, width int) string {
	if width < 1 || visibleLen(plain) <= width {
		return styled
	}
	return lipgloss.NewStyle().MaxWidth(width).Render(styled)
}
