package main

import (
	"context"
	"fmt"
	"io"
	"runtime"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/teleport"
	"github.com/vulnetix/belai/internal/trustgate"
	"github.com/vulnetix/belai/internal/tui"
	"github.com/vulnetix/belai/internal/version"
)

// runTeleport continues a session of the account on this host (docs/teleport.md):
// it asks the backend, writes the new session and returns the TUI's options for
// it. workdir is a trusted directory. The session runs there, or in a worktree
// at the commit it was at, which this host trusts because it lies inside a
// repository the user already trusted.
func runTeleport(ctx context.Context, workdir, ref, override string, stderr io.Writer) (teleport.Result, *tui.Teleported, error) {
	client, err := rcClient(workdir)
	if err != nil {
		return teleport.Result{}, nil, fmt.Errorf("teleport: %w", err)
	}
	dir, err := config.GlobalDir()
	if err != nil {
		return teleport.Result{}, nil, fmt.Errorf("teleport: %w", err)
	}
	hostID, err := sessionsync.HostID(dir)
	if err != nil {
		return teleport.Result{}, nil, fmt.Errorf("teleport: %w", err)
	}
	store, err := session.NewStore()
	if err != nil {
		return teleport.Result{}, nil, fmt.Errorf("teleport: %w", err)
	}
	res, err := teleport.Run(ctx, teleport.Options{
		Ref:         ref,
		RefOverride: override,
		Workdir:     workdir,
		API:         client,
		HostID:      hostID,
		Host:        sessionsync.Host{Hostname: sessionsync.Hostname(), OS: runtime.GOOS, BelaiVersion: version.Version},
		Store:       store,
		Trust:       func(dir string) error { return trustgate.Grant(dir, nil) },
		Progress:    func(s string) { fmt.Fprintln(stderr, "teleport:", s) },
	})
	if err != nil {
		return res, nil, fmt.Errorf("teleport: %w", err)
	}
	return res, &tui.Teleported{OriginID: res.OriginID, SessionID: res.SessionID, Notices: res.Notices}, nil
}
