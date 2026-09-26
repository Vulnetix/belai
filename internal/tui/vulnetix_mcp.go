package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/credentials"
	"github.com/vulnetix/belai/internal/mcp"
)

// vulnetixMCPDoneMsg reports a /vulnetix mcp install or removal.
type vulnetixMCPDoneMsg struct {
	removed bool
	tools   int
	err     error
}

// vulnetixMCPCommand runs /vulnetix mcp [install | remove | status].
func (a *App) vulnetixMCPCommand(args []string) tea.Cmd {
	sub := "install"
	if len(args) > 0 {
		sub = strings.ToLower(args[0])
	}
	switch sub {
	case "install", "add", "on":
		a.addSystem("vulnetix mcp: connecting " + config.VulnetixMCPURL + "…")
		ctx, wd := a.ctx, a.workdir
		return func() tea.Msg {
			n, err := installVulnetixMCP(ctx, wd)
			return vulnetixMCPDoneMsg{tools: n, err: err}
		}
	case "remove", "off", "uninstall":
		wd := a.workdir
		return func() tea.Msg {
			return vulnetixMCPDoneMsg{removed: true, err: removeVulnetixMCP(wd)}
		}
	case "status":
		for _, s := range mcp.Active().Status() {
			if s.Name == config.VulnetixMCPName {
				a.addSystem(mcpListing([]mcp.Status{s}))
				return nil
			}
		}
		a.addSystem("vulnetix mcp: not installed · /vulnetix mcp to add it")
		return nil
	default:
		a.addSystem("usage: /vulnetix mcp [install | remove | status]")
		return nil
	}
}

// installVulnetixMCP writes the Vulnetix server to the user's global
// settings and connects it in this session. The CLI credential is checked
// first but never written: the entry carries config.VulnetixCLIRef, which the
// MCP manager resolves on every dial. It returns the server's tool count.
func installVulnetixMCP(ctx context.Context, workdir string) (int, error) {
	if _, err := credentials.VulnetixAuthHeader(workdir); err != nil {
		return 0, fmt.Errorf("no Vulnetix CLI credential (%v) · run /vulnetix setup or `vulnetix auth login`", err)
	}
	entry := config.VulnetixMCPServer()
	if err := config.Mutate(config.ScopeGlobal, workdir, func(s *config.Settings) error {
		if s.MCP == nil {
			s.MCP = &config.MCPSettings{}
		}
		if s.MCP.Servers == nil {
			s.MCP.Servers = map[string]config.MCPServer{}
		}
		s.MCP.Servers[config.VulnetixMCPName] = entry
		return nil
	}); err != nil {
		return 0, err
	}
	m := mcp.Active()
	if m == nil {
		return 0, nil
	}
	if err := m.Upsert(ctx, config.VulnetixMCPName, entry); err != nil {
		return 0, err
	}
	n := 0
	for _, s := range m.Status() {
		if s.Name == config.VulnetixMCPName {
			n = len(s.Tools)
		}
	}
	return n, nil
}

// removeVulnetixMCP deletes the Vulnetix server from the global settings and
// stops it.
func removeVulnetixMCP(workdir string) error {
	if err := config.Mutate(config.ScopeGlobal, workdir, func(s *config.Settings) error {
		if s.MCP != nil {
			delete(s.MCP.Servers, config.VulnetixMCPName)
		}
		return nil
	}); err != nil {
		return err
	}
	mcp.Active().Remove(config.VulnetixMCPName)
	return nil
}

// handleVulnetixMCPDone reports the outcome and rebuilds the session so the
// next turn sees the server's current tools.
func (a *App) handleVulnetixMCPDone(m vulnetixMCPDoneMsg) {
	_ = a.reloadSettings()
	a.invalidateAgentSession()
	switch {
	case m.err != nil && m.removed:
		a.addSystem("vulnetix mcp: remove failed: " + mcpClean(m.err.Error(), 200))
	case m.err != nil:
		a.addSystem("vulnetix mcp: " + mcpClean(m.err.Error(), 240))
	case m.removed:
		a.addSystem("vulnetix mcp: removed from global settings")
	default:
		a.addSystem(fmt.Sprintf("vulnetix mcp: connected · %d tools · authenticated with the Vulnetix CLI credential · /vulnetix mcp remove to undo", m.tools))
	}
}
