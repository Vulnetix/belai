package components

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/signet/internal/todos"
)

// TodoPanel renders the shared goal/plan todo list: the previous completed
// item, the current item highlighted, the next item, and how many more remain.
// It is a flat panel, never nested; when the list is empty it renders nothing.
type TodoPanel struct {
	Width int
	List  *todos.List
}

// View renders the panel, or "" when there is nothing to show.
func (p TodoPanel) View() string {
	if p.List == nil || len(p.List.Items) == 0 {
		return ""
	}
	prev, current, next, more := p.List.Window()
	inner := max(p.Width-4, 1)

	var b strings.Builder
	if prev != nil {
		fmt.Fprintf(&b, "%s\n", MutedStyle.Render("✓ "+truncateRunes(prev.Text, inner-2)))
	}
	if current != nil {
		b.WriteString(EmphStyle.Render("▸ "+truncateRunes(current.Text, inner-2)) + "\n")
	}
	if next != nil {
		fmt.Fprintf(&b, "%s\n", MutedStyle.Render("  "+truncateRunes(next.Text, inner-2)))
	}
	if more > 0 {
		fmt.Fprintf(&b, "%s\n", MutedStyle.Render(fmt.Sprintf("  … %d more", more)))
	}
	body := strings.TrimRight(b.String(), "\n")

	return Panel{
		Title:  "todo",
		Body:   body,
		Width:  p.Width,
		Accent: lipgloss.TerminalColor(ColorAmber),
		Raw:    true,
	}.View()
}
