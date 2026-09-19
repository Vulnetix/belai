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
	if mouseEnabled(opts.Settings) {
		progOpts = append(progOpts, tea.WithMouseCellMotion())
	}
	if kittyEnabled(opts.Settings) {
		progOpts = append(progOpts,
			tea.WithOutput(&keys.Writer{File: os.Stdout, Flags: 1}),
			tea.WithFilter(keys.Filter))
		defer os.Stdout.WriteString(keys.Pop)
	}
	p := tea.NewProgram(New(opts), progOpts...)
	model, err := p.Run()
	// Only a clean exit prints the card. On an error path main.go keeps its
	// stderr-and-exit-1 behaviour; nothing is written to stdout then.
	if err == nil {
		if a, ok := model.(*App); ok {
			a.ensureSessionName()
			_, _ = os.Stdout.WriteString(a.exitCard().View() + "\n")
		}
	}
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

// mouseEnabled reports whether the TUI captures the mouse. Default true.
// With capture on the terminal's own selection is unavailable, so the TUI
// implements its own: left-button press-drag-release over the transcript
// selects a character range and copies the clean underlying text on release
// (see internal/tui/selection.go). shift+drag is not an escape hatch.
func mouseEnabled(s *config.Settings) bool {
	if s != nil && s.UI != nil && s.UI.Mouse != nil {
		return *s.UI.Mouse
	}
	return true
}
