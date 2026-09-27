package main

import (
	"context"
	"os"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/credentials"
	"github.com/vulnetix/belai/internal/httpclient"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// cliKanban opens the global board for a headless or ACP session, or returns
// nils when the kanban setting is off. A session with no id yet gets one for
// the provenance its writes carry.
func cliKanban(workdir, sessionID string, settings config.Settings) (*kanban.Store, *kanban.Source) {
	if !settings.KanbanEnabled() {
		return nil, nil
	}
	store, err := kanban.OpenDefault()
	if err != nil {
		return nil, nil
	}
	if sessionID == "" {
		sessionID = session.MustID()
	}
	hostID := ""
	if dir, err := config.GlobalDir(); err == nil {
		if id, err := sessionsync.HostID(dir); err == nil {
			hostID = id
		}
	}
	return store, kanban.NewSource(kanban.ProvenanceFor(workdir, sessionID, hostID))
}

// flushKanban pushes the board's unsynced changes once, best effort, when the
// kanban and sync settings are on and a usable Vulnetix CLI credential
// resolves. A headless run never pulls in the background; the next TUI does.
func flushKanban(settings config.Settings, workdir string) {
	if !settings.KanbanEnabled() || !settings.SyncEnabled() {
		return
	}
	store, err := kanban.OpenDefault()
	if err != nil {
		return
	}
	if out, err := store.Outbox(); err != nil || len(out) == 0 {
		return
	}
	header, err := credentials.VulnetixAuthHeader(workdir)
	if err != nil || sessionsync.UsableCredential(header) != nil {
		return
	}
	client, err := sessionsync.NewClient(sessionsync.BaseURL(os.Getenv("VULNETIX_WEB_URL")),
		func() (string, error) { return header, nil }, httpclient.Default())
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = kanban.NewSyncer(store, client, kanban.SyncOptions{}).Flush(ctx)
}
