package tui

import (
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/tui/keys"
)

// Start runs the TUI until the user quits. It requires a TTY.
func Start(opts Options) error {
	progOpts := []tea.ProgramOption{tea.WithAltScreen()}
	if kittyEnabled(opts.Settings) {
		progOpts = append(progOpts,
			tea.WithOutput(&keys.Writer{File: os.Stdout, Flags: 1}),
			tea.WithFilter(keys.Filter))
		defer os.Stdout.WriteString(keys.Pop)
	}
	p := tea.NewProgram(New(opts), progOpts...)
	_, err := p.Run()
	return err
}

func kittyEnabled(s *config.Settings) bool {
	if os.Getenv("SIGNET_NO_KITTY") == "1" {
		return false
	}
	if s != nil && s.UI != nil && s.UI.KittyKeyboard != nil {
		return *s.UI.KittyKeyboard
	}
	return true
}
