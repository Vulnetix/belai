package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/egress"
	"github.com/vulnetix/belai/internal/sanitize"
)

// egressCommand is /egress: the sandbox's network mode, the hosts the egress
// proxy lets a command reach, and the connections it refused. `allow HOST` and
// `revoke HOST` change the session's own list. Only a person typing here reaches
// them: no tool, settings layer or web session can add an entry, and every entry
// passes the same validation as a settings entry.
func (a *App) egressCommand(arg string) tea.Cmd {
	f := strings.Fields(arg)
	reg := egress.Default
	switch {
	case len(f) == 0 || (len(f) == 1 && f[0] == "list"):
		a.addSystem(egressReport(a.settings.Sandbox, reg))
	case len(f) == 2 && f[0] == "allow":
		e, err := reg.AllowSession(f[1])
		if err != nil {
			a.addSystem("egress: not allowed: " + sanitize.Line(err.Error(), 200))
			return nil
		}
		msg := "egress: allowed " + e + " for this session"
		if a.settings.Sandbox.NetworkOr() != config.SandboxNetworkProxy {
			msg += " (sandbox.network is not proxy, so nothing uses the list yet)"
		}
		a.addSystem(msg)
	case len(f) == 2 && f[0] == "revoke":
		if reg.RevokeSession(f[1]) {
			a.addSystem("egress: revoked " + sanitize.Line(f[1], 120) + " for this session")
		} else {
			a.addSystem("egress: " + sanitize.Line(f[1], 120) + " is not a session entry (an entry from settings is removed there)")
		}
	default:
		a.addSystem("usage: /egress [list] | /egress allow HOST | /egress revoke HOST   (HOST is a name or *.domain)")
	}
	return nil
}

// egressReport composes the /egress text. Every host is cleaned to one line.
func egressReport(s *config.SandboxSettings, reg *egress.Registry) string {
	var b strings.Builder
	mode := s.NetworkOr()
	fmt.Fprintf(&b, "egress: sandbox.network is %s", mode)
	if mode != config.SandboxNetworkProxy {
		b.WriteString(" (the egress proxy is used only when it is proxy)")
	}
	list := s.EgressAllowList()
	b.WriteString("\n  settings (sandbox.egress_allow): ")
	b.WriteString(joinHosts(list))
	b.WriteString("\n  session (/egress allow): ")
	b.WriteString(joinHosts(reg.SessionAllowed()))
	den := reg.Denials()
	fmt.Fprintf(&b, "\n  refused: %d connection(s) this run", reg.Total())
	if len(den) > 0 {
		b.WriteString(", most recent first:")
		for i, d := range den {
			if i == 10 {
				fmt.Fprintf(&b, "\n    ... and %d more", len(den)-i)
				break
			}
			host := d.Host
			if host == "" {
				host = "(no host)"
			}
			fmt.Fprintf(&b, "\n    %s:%d  %s  x%d", sanitize.Line(host, 120), d.Port, d.Reason, d.Count)
		}
		b.WriteString("\n  allow one with /egress allow HOST, then run the command again")
	}
	return b.String()
}

func joinHosts(h []string) string {
	if len(h) == 0 {
		return "none"
	}
	clean := make([]string, len(h))
	for i, e := range h {
		clean[i] = sanitize.Line(e, 120)
	}
	return strings.Join(clean, ", ")
}
