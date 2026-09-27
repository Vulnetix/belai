package main

import (
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/headless"
	"github.com/vulnetix/belai/internal/kanban"
)

// cliKanban opens the global board for a headless or ACP session, or returns
// nils when the kanban setting is off. A session with no id yet gets one for
// the provenance its writes carry.
func cliKanban(workdir, sessionID string, settings config.Settings) (*kanban.Store, *kanban.Source) {
	return headless.Kanban(workdir, sessionID, settings)
}

// flushKanban pushes the board's unsynced changes once, best effort, when the
// kanban and sync settings are on and a usable Vulnetix CLI credential
// resolves. A headless run never pulls in the background; the next TUI does.
func flushKanban(settings config.Settings, workdir string) {
	headless.FlushKanban(settings, workdir)
}
