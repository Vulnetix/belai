package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/signet/internal/tui/components"
)

// resumeCompactView renders the post-resume compaction offer.
func (a *App) resumeCompactView() string {
	w := a.contentWidth()
	body := "This session is near its context window. Compact it into a new session now?\n\nCompacting forks the session: a new session id is minted and the resumed session is left intact as its parent."
	head := components.SectionHeader("Resume — compact?", "esc skip", w)
	panel := components.Panel{Title: "compaction", Body: body, Width: w}.View()
	help := components.HelpBar("enter", "compact", "esc/n", "skip")
	return lipgloss.NewStyle().Padding(1).Render(head + panel + "\n" + help + "\n")
}

func (a *App) handleResumeCompactKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.String() {
	case "enter":
		a.pop()
		return a, a.compactCmd()
	case "esc", "n":
		a.pop()
		return a, nil
	}
	return a, nil
}
