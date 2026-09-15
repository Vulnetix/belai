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

var roleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))

// View renders all messages with a role label.
func (m MessageList) View() string {
	var b strings.Builder
	for _, msg := range m.Messages {
		if msg.Role == "tool" {
			b.WriteString(roleStyle.Render("[tool] ") + msg.ToolName + " " + msg.ToolArgs)
			if msg.Status != "" {
				b.WriteString(" " + msg.Status)
			}
			b.WriteString("\n\n")
			continue
		}
		b.WriteString(roleStyle.Render("[" + msg.Role + "] "))
		b.WriteString(msg.Content)
		b.WriteString("\n\n")
	}
	return b.String()
}
