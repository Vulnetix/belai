package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// Start runs the TUI until the user quits. It requires a TTY.
func Start(opts Options) error {
	p := tea.NewProgram(New(opts), tea.WithAltScreen())
	_, err := p.Run()
	return err
}
