package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/run"
)

// warmDocs classifies the project's prose files in the background at startup
// when a SystemOne decision backend answers the security checks, so a later
// Read of any of them needs no classifier call (agent.WarmDocs). Its outcome
// reaches the thread as a role-manager activity; nothing comes back here.
func (a *App) warmDocs() tea.Cmd {
	if a.cfg.Provider == "" || a.workdir == "" {
		return nil
	}
	cfg, client, cache, root := a.cfg, a.client, a.cache, a.workdir
	return func() tea.Msg {
		agent.WarmDocs(context.Background(), run.NewPipeline(cfg, client, cache), root)
		return nil
	}
}
