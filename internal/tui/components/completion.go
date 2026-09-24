package components

import "strings"

// CompletionTitle is the title of the agent-mode completion panel. The panel
// is composed by the harness, so its wording is the same on every turn.
const CompletionTitle = "✓ done"

// completionPanel renders the harness-composed panel that closes an
// agent-mode turn. The body is one line of harness-observed facts; the frame
// is the primary accent so the end of a turn reads at a glance.
func completionPanel(msg Message, width int) (string, LineMap) {
	p := Panel{
		Title:  CompletionTitle,
		Body:   strings.TrimSpace(msg.Text()),
		Width:  width,
		Accent: ColorTeal,
	}
	return p.Render()
}
