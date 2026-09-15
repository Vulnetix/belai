package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/signet/internal/tui/components"
)

func (a *App) agentView() string {
	w := a.contentWidth()
	var b strings.Builder
	b.WriteString(components.SectionHeader("Background Agents", "esc back", w))
	if a.bgManager == nil {
		b.WriteString(components.MutedStyle.Render("  no background manager configured") + "\n")
	} else {
		list := a.bgManager.List()
		if len(list) == 0 {
			b.WriteString(components.MutedStyle.Render("  no active agents") + "\n")
		} else {
			for _, s := range list {
				b.WriteString(fmt.Sprintf("  %-12s %-10s %-10s iter=%d\n", s.Name, s.Mode, s.State, s.Iteration))
				if s.LastOutput != "" {
					out := s.LastOutput
					if len(out) > w-6 {
						out = out[:w-6] + "…"
					}
					b.WriteString(components.MutedStyle.Render("    "+out) + "\n")
				}
			}
		}
	}
	b.WriteString("\n" + components.HelpBar("esc", "back") + "\n")
	return lipgloss.NewStyle().Padding(1).Render(b.String())
}

func (a *App) handleAgentKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.String() {
	case "esc":
		a.pop()
		return a, nil
	}
	return a, nil
}
