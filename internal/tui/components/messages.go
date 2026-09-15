// Package components holds the Bubble Tea building blocks for the Signet TUI.
package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/vulnetix/signet/internal/transcript"
)

// Message is one message in the transcript.
type Message struct {
	Role    string // user, assistant, tool, system
	Content string
	Usage   *transcript.Usage // non-nil on metered assistant turns
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
		b.WriteString(roleStyle.Render("[" + msg.Role + "] "))
		b.WriteString(msg.Content)
		b.WriteString("\n\n")
	}
	return b.String()
}
