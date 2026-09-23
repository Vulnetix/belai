package tui

import (
	"context"

	"github.com/vulnetix/signet/internal/calltrace"
)

// publishSessionID pushes the current session id into the background agent
// and process managers, so their turns stamp the same X-Signet-Session-Id and
// traceparent as the foreground session. Call it wherever a.sessionID changes
// and after either manager is created. It runs on the Bubble Tea goroutine;
// the managers store the id atomically.
func (a *App) publishSessionID() {
	if a.bgManager != nil {
		a.bgManager.SetSessionID(a.sessionID)
	}
	if a.procManager != nil {
		a.procManager.SetSessionID(a.sessionID)
	}
}

// toolContext returns ctx carrying the session id and the named tool, for a
// tool the TUI runs directly (inline !cmd, @file admission, the file picker)
// rather than through an agent turn. Call it on the Bubble Tea goroutine and
// capture the result: it reads a.sessionID.
func (a *App) toolContext(ctx context.Context, tool, callID string) context.Context {
	return calltrace.WithTool(calltrace.WithSession(ctx, a.sessionID), tool, callID)
}
