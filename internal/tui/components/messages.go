// Package components holds the Bubble Tea building blocks for the Signet TUI.
package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/vulnetix/signet/internal/transcript"
)

// Message is one message in the transcript.
type Message struct {
	Role     string // user, assistant, tool, system
	Content  string
	Usage    *transcript.Usage // non-nil on metered assistant turns
	ToolName string            // set on tool turns
	ToolArgs string            // set on tool turns
	Status   string            // set on tool turns (✓, withheld, …)
}

// MessageList renders the transcript.
type MessageList struct {
	Messages []Message
	Width    int
}

const messageMinWidth = 32

// View renders the transcript: conversational turns as flat titled panels,
// tool calls and system notices as single-line rows between them.
func (m MessageList) View() string {
	width := max(m.Width, messageMinWidth)

	var b strings.Builder
	for _, msg := range m.Messages {
		switch msg.Role {
		case "tool":
			b.WriteString(toolRow(msg, width))
		case "system":
			b.WriteString(systemRow(msg.Content, width))
		default:
			b.WriteString(turnPanel(msg, width))
		}
		b.WriteString("\n\n")
	}
	return b.String()
}

// turnPanel renders a user or assistant turn.
func turnPanel(msg Message, width int) string {
	title, accent := "signet", lipgloss.TerminalColor(ColorTeal)
	if msg.Role == "user" {
		title, accent = "you", lipgloss.TerminalColor(ColorTealSoft)
	} else if msg.Role != "assistant" {
		title, accent = msg.Role, lipgloss.TerminalColor(ColorMuted)
	}

	meta := ""
	if msg.Usage != nil {
		if n := msg.Usage.Total(); n > 0 {
			meta = formatTokens(n) + " tok"
		}
	}

	return Panel{
		Title:  title,
		Meta:   meta,
		Body:   strings.TrimRight(msg.Content, "\n"),
		Width:  width,
		Accent: accent,
	}.View()
}

// toolRow renders one tool call as a flat row — tool activity is subordinate
// to the turn that caused it, so it never gets a frame of its own.
func toolRow(msg Message, width int) string {
	head := WarnStyle.Render("⌁ ") + EmphStyle.Render(msg.ToolName)
	plain := "⌁ " + msg.ToolName

	if args := strings.TrimSpace(msg.ToolArgs); args != "" {
		args = strings.ReplaceAll(args, "\n", " ")
		head += "  " + MutedStyle.Render(args)
		plain += "  " + args
	}

	status := strings.TrimSpace(msg.Status)
	if status == "" {
		return truncateLine(head, plain, width)
	}

	pad := width - visibleLen(plain) - visibleLen(status)
	if pad < 1 {
		// No room for both: the status is the part worth keeping.
		trimTo := width - visibleLen(status) - 2
		head = truncateLine(head, plain, trimTo)
		plain = truncateRunes(plain, max(trimTo, 1))
		pad = max(width-visibleLen(plain)-visibleLen(status), 1)
	}
	return head + spaces(pad) + statusStyle(status).Render(status)
}

// systemRow renders a system notice as a dim, marked line.
func systemRow(content string, width int) string {
	body := strings.TrimRight(content, "\n")
	marker := MutedStyle.Render("│ ")
	wrapped := lipgloss.NewStyle().Foreground(ColorMuted).Width(max(width-2, 8)).Render(body)

	var b strings.Builder
	for i, line := range strings.Split(wrapped, "\n") {
		if i > 0 {
			b.WriteString("\n")
		}
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
