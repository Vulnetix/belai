package tui

import (
	"context"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/activity"
	"github.com/vulnetix/belai/internal/selfupdate"
)

// belaiUpdateMsg carries the result of the startup release check.
type belaiUpdateMsg struct {
	status selfupdate.Status
}

// checkBelaiUpdateCmd compares this binary's version against the newest
// GitHub release, off the first frame like the repo map and the Vulnetix
// probe. It returns nil when the check is disabled, so no goroutine and no
// request happen at all. The check is read-only: it never downloads or
// installs anything, it only renders the command the user would run.
func (a *App) checkBelaiUpdateCmd() tea.Cmd {
	if !selfupdate.Enabled(os.Getenv, a.settings.UpdateCheckEnabled()) {
		return nil
	}
	return func() tea.Msg {
		var h *activity.Handle
		if a.activity != nil {
			h = a.activity.Add(activity.Activity{
				Kind:   activity.KindShell,
				Label:  "belai release check",
				Argv:   []string{"selfupdate.Check"},
				State:  activity.StateRunning,
				Silent: true,
			}, nil)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		st := selfupdate.Check(ctx, selfupdate.Options{Client: a.client})
		if h != nil {
			h.Finish(0, false, nil)
		}
		return belaiUpdateMsg{status: st}
	}
}

// handleBelaiUpdate stores the check result. A newer release adds one belai
// panel notice; a failed or negative check stays silent, because a startup
// that cannot reach GitHub is not the user's problem to read about.
func (a *App) handleBelaiUpdate(m belaiUpdateMsg) tea.Cmd {
	a.belaiUpdate = m.status
	if notice := m.status.Notice(); notice != "" {
		a.addSystem(notice)
		// The banner grew a note on its version row: it is memoised by
		// width, so invalidate the cached height measurement.
		a.bannerW = -1
	}
	return nil
}
