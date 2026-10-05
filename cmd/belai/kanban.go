package main

import (
	"context"
	"time"

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

// kanbanFirstSync bounds the pull that runs before a session's first turn, so a
// slow or unreachable website cannot hold the turn up.
const kanbanFirstSync = 10 * time.Second

// startKanbanSync keeps a long-lived headless session's board in step with the
// website's. A web session (belai rc-session) runs for hours, and a card filed on
// the website after it started is on the website board but not on the host's
// unless something pulls it. It pulls once, synchronously, so the board is there
// for the first turn, then runs the same push and pull loop the TUI runs until
// ctx ends or the returned stop is called. stop ends the loop and pushes what the
// session changed once more, best effort.
//
// It is for a session that outlives a turn. A one-shot run flushes at exit
// (flushKanban) and never pulls; a fleet worker pulls per poll (PullKanban).
func startKanbanSync(ctx context.Context, board *kanban.Store, remote kanban.Remote) (stop func()) {
	s := kanban.NewSyncer(board, remote, kanban.SyncOptions{})
	first, cancelFirst := context.WithTimeout(ctx, kanbanFirstSync)
	s.Sync(first)
	cancelFirst()
	loop, cancel := context.WithCancel(ctx)
	s.Start(loop)
	return func() {
		cancel()
		// ctx may already be done (a stop from the website cancels it), so the
		// final push has a context of its own.
		last, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = s.Flush(last)
	}
}
