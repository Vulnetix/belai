package tui

import (
	"context"
	"fmt"

	"github.com/vulnetix/signet/internal/agent"
	"github.com/vulnetix/signet/internal/hooks"
)

// sessionHooks returns the hooks the TUI fires itself (session_start,
// session_end, notification). The agent session loads its own copy for the
// tool and turn events; both read the same directory under the same posture.
func (a *App) sessionHooks() *hooks.Set {
	if a.hookSet == nil {
		a.hookSet = agent.LoadHooks(a.settings, a.effectivePosture())
		if a.hookSet == nil {
			a.hookSet = &hooks.Set{}
		}
	}
	return a.hookSet
}

// fireSessionHook runs a session-level hook event synchronously. These events
// cannot block anything and their output is not read: the TUI owns the
// screen, so failures are recorded in the trace only.
func (a *App) fireSessionHook(event string) {
	a.fireHook(hooks.Input{Event: event})
}

// fireHook runs one TUI-owned hook event and traces the outcome.
func (a *App) fireHook(in hooks.Input) {
	hs := a.sessionHooks()
	if !hs.Has(in.Event) {
		return
	}
	in.SessionID, in.Cwd = a.sessionID, a.workdir
	o := hs.Dispatch(context.Background(), in)
	a.traceRecord("hook", o.Decision, in.Event, fmt.Sprintf("ran=%d failed=%d", o.Ran, len(o.Failures)), 0)
}
