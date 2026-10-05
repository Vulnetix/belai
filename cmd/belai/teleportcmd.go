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
func runTeleport(ctx context.Context, workdir, ref, override string, push bool, providerFlag, modelFlag string, stderr io.Writer) (teleport.Result, *tui.Teleported, error) {
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
	// The user's agreement that the origin host may push a teleport branch is the
	// flag, or their own teleport.push setting here. The origin host decides for
	// itself by its own setting too (docs/teleport.md).
	if settings, serr := config.LoadMerged(workdir); serr == nil && settings.TeleportPushPolicy() == config.TeleportPushAllow {
		push = true
	}
	res, err := teleport.Run(ctx, teleport.Options{
		Ref:         ref,
		Push:        push,
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
	// The code the forge could not carry is replayed here before the terminal UI
	// opens, so the session starts on the finished checkout. The outcome is a
	// notice, never a failure: the teleported session exists either way.
	if res.Replay != nil {
		out := runTeleportReplay(ctx, res.Replay, providerFlag, modelFlag, stderr)
		res.Notices = append(res.Notices, out.Summary)
	}
	return res, &tui.Teleported{OriginID: res.OriginID, SessionID: res.SessionID, Notices: res.Notices}, nil
}
