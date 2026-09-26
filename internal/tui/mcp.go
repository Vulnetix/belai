package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/mcp"
	"github.com/vulnetix/signet/internal/sanitize"
)

// mcpDoneMsg reports a /mcp restart.
type mcpDoneMsg struct {
	name string
	err  error
}

// mcpCommand runs /mcp: list servers, or restart one off the UI goroutine.
func (a *App) mcpCommand(arg string) tea.Cmd {
	m := mcp.Active()
	fields := strings.Fields(arg)
	if len(fields) == 2 && fields[0] == "restart" {
		if m == nil {
			a.addSystem("mcp: no servers are configured")
			return nil
		}
		name, ctx := fields[1], a.ctx
		a.addSystem("mcp: restarting " + name + "…")
		return func() tea.Msg { return mcpDoneMsg{name: name, err: m.Restart(ctx, name)} }
	}
	if len(fields) > 0 && fields[0] != "list" {
		a.addSystem("usage: /mcp [list | restart <name>]")
		return nil
	}
	a.addSystem(mcpListing(m.Status()))
	return nil
}

// mcpListing renders server states. Server errors and stderr are the
// server's text, so they are sanitized and flattened.
func mcpListing(st []mcp.Status) string {
	if len(st) == 0 {
		return "no MCP servers configured · add them under mcp.servers in your global settings.json (docs/mcp.md)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d MCP server(s):", len(st))
	for _, s := range st {
		fmt.Fprintf(&b, "\n  %s (%s) · %s", s.Name, s.Transport, s.State)
		if s.Err != "" {
			fmt.Fprintf(&b, " · %s", mcpClean(s.Err, 200))
		}
		if len(s.Tools) > 0 {
			fmt.Fprintf(&b, "\n    %s", strings.Join(s.Tools, ", "))
		}
	}
	b.WriteString("\n  tools from a server that connected after this session started appear after /clear")
	return b.String()
}

func mcpClean(s string, n int) string {
	s = strings.Join(strings.Fields(sanitize.Sanitize(s)), " ")
	if r := []rune(s); len(r) > n {
		s = string(r[:n]) + "…"
	}
	return s
}
