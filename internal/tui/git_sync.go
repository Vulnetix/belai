package tui

import (
	"context"
	"encoding/json"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/gitsync"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// startGitSync creates the session's git sync and starts reading the
// repository for the website. Only a real run calls it; New alone (every test)
// never execs git. The sync starts on or off as git.sync says, and /gitsync
// changes it for the session from there.
func (a *App) startGitSync() {
	if a.gitSync != nil {
		return
	}
	a.gitSync = gitsync.New(a.workdir, a.settings.GitSyncEnabled(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	a.gitCancel = cancel
	go a.gitSync.Watch(ctx, gitsync.InfoEvery, nil)
}

// stopGitSync ends the repository reading started by startGitSync.
func (a *App) stopGitSync() {
	if a.gitCancel != nil {
		a.gitCancel()
		a.gitCancel = nil
	}
}

// gitSyncRaw is the session syncer's source for the repository reading: the
// cached JSON of the last read, nil when there is none.
func (a *App) gitSyncRaw() func() json.RawMessage {
	if a.gitSync == nil {
		return nil
	}
	return a.gitSync.Raw
}

// gitSyncControls applies a switch the website made on the live session. It
// runs on the syncer's goroutine, so it touches only the sync's own state;
// the new switch reaches the website with the next reading.
func (a *App) gitSyncControls() func(string, sessionsync.Controls) {
	gs := a.gitSync
	if gs == nil {
		return nil
	}
	return func(_ string, c sessionsync.Controls) {
		if c.GitSync != nil && gs.Enabled() != *c.GitSync {
			gs.SetEnabled(*c.GitSync)
			gs.Kick()
		}
	}
}

// gitSyncCommand implements /gitsync [status|on|off|global on|global off].
func (a *App) gitSyncCommand(arg string) tea.Cmd {
	f := strings.Fields(arg)
	switch {
	case len(f) == 0 || f[0] == "status":
		a.addSystem(a.gitSyncStatusText())
	case len(f) == 1 && (f[0] == "on" || f[0] == "off"):
		a.setGitSync(f[0] == "on", false)
	case len(f) == 2 && f[0] == "global" && (f[1] == "on" || f[1] == "off"):
		a.setGitSync(f[1] == "on", true)
	default:
		a.addSystem("usage: /gitsync [status|on|off|global on|global off]")
	}
	return nil
}

// setGitSync switches the sync for this session, and with global also for
// every later session by writing git.sync to the user's settings.
func (a *App) setGitSync(on, global bool) {
	if a.gitSync == nil {
		a.addSystem("git sync is not running in this session")
		return
	}
	a.gitSync.SetEnabled(on)
	a.gitSync.Kick()
	word := map[bool]string{true: "on", false: "off"}[on]
	if !global {
		a.addSystem("git sync " + word + " for this session. /gitsync global " + word + " makes it the default for future sessions")
		return
	}
	if err := config.Mutate(config.ScopeGlobal, a.workdir, func(s *config.Settings) error {
		if s.Git == nil {
			s.Git = &config.GitSettings{}
		}
		s.Git.Sync = &on
		return nil
	}); err != nil {
		a.addSystem("git sync is " + word + " for this session, but the setting was not saved: " + err.Error())
		return
	}
	if err := a.reloadSettings(); err != nil {
		a.addSystem("git sync: " + err.Error())
	}
	a.addSystem("git sync " + word + " for this session and for future sessions (git.sync in your settings)")
}

func (a *App) gitSyncStatusText() string {
	if a.gitSync == nil {
		return "git sync is not running in this session"
	}
	var b strings.Builder
	if a.gitSync.Enabled() {
		b.WriteString("git sync on: before the first turn, and before the first turn after a commit, the branch is rebased onto origin's default branch when the tree is clean")
	} else {
		b.WriteString("git sync off for this session")
	}
	if !a.settings.GitSyncEnabled() {
		b.WriteString(" (git.sync is false in settings)")
	}
	if last := a.gitSync.Last(); last != nil {
		b.WriteString("\nlast: " + last.Line())
	}
	return b.String()
}
