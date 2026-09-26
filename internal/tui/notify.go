package tui

import (
	"context"
	"io"
	"os"
	"runtime"
	"slices"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/hooks"
	"github.com/vulnetix/belai/internal/notify"
)

// notifyCmd tells the user Belai needs them: a desktop notification when
// the settings ask for this event, and the notification hook either way.
// Both run off the UI goroutine. subject is a tool or agent name; notify
// reduces it to an identifier.
func (a *App) notifyCmd(event, subject string) tea.Cmd {
	ns := a.settings.Notifications
	want := ns.NotificationsEnabled() && notifyWanted(ns.Events, event)
	hookSet := a.sessionHooks()
	fireHook := hookSet.Has(hooks.EventNotification)
	if !want && !fireHook {
		return nil
	}
	backend := ""
	if ns != nil && notify.ValidBackend(ns.Backend) {
		backend = ns.Backend
	}
	sid, wd, ctx := a.sessionID, a.workdir, a.ctx
	a.traceRecord("notify", event, subject, backend, 0)
	return func() tea.Msg {
		if ctx == nil {
			ctx = context.Background()
		}
		if fireHook {
			hookSet.Dispatch(ctx, hooks.Input{Event: hooks.EventNotification, SessionID: sid, Cwd: wd, Notification: event})
		}
		if want {
			tty, closeTTY := notifyTTY()
			defer closeTTY()
			_ = notify.New(backend, notify.SystemEnv(), tty).Send(ctx, event, subject)
		}
		return nil
	}
}

// notifyTurnDone notifies turn_done when the turn outlasted the threshold.
func (a *App) notifyTurnDone(elapsed time.Duration) tea.Cmd {
	if elapsed < time.Duration(a.settings.Notifications.MinTurnSecondsOr())*time.Second {
		return nil
	}
	return a.notifyCmd(notify.EventTurnDone, "")
}

func notifyWanted(events []string, event string) bool {
	if events == nil {
		events = notify.DefaultEvents
	}
	return slices.Contains(events, event)
}

// notifyTTY opens the controlling terminal for escape sequences, so they go
// straight to the terminal rather than through the renderer (which strips
// OSC from anything it draws). It falls back to stdout.
func notifyTTY() (io.Writer, func()) {
	if runtime.GOOS != "windows" {
		if f, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0); err == nil {
			return f, func() { _ = f.Close() }
		}
	}
	return os.Stdout, func() {}
}
