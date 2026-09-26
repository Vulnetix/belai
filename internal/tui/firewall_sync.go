package tui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/aifirewall"
	"github.com/vulnetix/belai/internal/vulnetixcli"
)

// firewallSyncTimeout bounds one `vulnetix ai-firewall key set`.
const firewallSyncTimeout = 30 * time.Second

// firewallSyncMsg reports one provider key pushed to the AI Firewall.
type firewallSyncMsg struct {
	provider string
	err      error
}

// syncFirewallKey pushes provider's API key to the org's AI Firewall (BYOK)
// in the background, so a routed turn is not refused with
// provider_key_missing. It runs only while the firewall is on, the provider
// has a gateway slug, a Vulnetix credential loads and the CLI is installed.
// The key reaches the CLI on stdin, never its argv or environment.
func (a *App) syncFirewallKey(provider string) tea.Cmd {
	if a.resolver == nil || !a.firewallEnabled() {
		return nil
	}
	st := a.resolver.FirewallState(provider)
	if !st.Routable || !st.HasCred {
		return nil
	}
	key, _, ok := a.resolver.Lookup(provider, "api_key")
	if !ok || strings.TrimSpace(key) == "" {
		return nil
	}
	cli, err := vulnetixcli.Detect()
	if err != nil {
		return nil
	}
	slug, ctx := st.Slug, a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return func() tea.Msg {
		c := *cli
		c.Timeout = firewallSyncTimeout
		c.Stdin = strings.NewReader(key + "\n")
		res, err := c.Exec(ctx, "--disable-memory", "ai-firewall", "key", "set", slug, "--stdin", "-o", "json")
		if err != nil {
			if msg := strings.TrimSpace(res.Stderr); msg != "" {
				err = cliError(msg)
			}
		}
		return firewallSyncMsg{provider: provider, err: err}
	}
}

// syncAllFirewallKeys pushes every configured provider's key.
func (a *App) syncAllFirewallKeys() tea.Cmd {
	if a.resolver == nil {
		return nil
	}
	var cmds []tea.Cmd
	for _, p := range a.resolver.ConfiguredProviders() {
		if _, ok := aifirewall.Slug(p); ok {
			cmds = append(cmds, a.syncFirewallKey(p))
		}
	}
	return tea.Batch(cmds...)
}

// handleFirewallSync records the outcome. A failed push marks the provider
// unroutable, so turns keep going direct instead of to a gateway that would
// refuse them.
func (a *App) handleFirewallSync(m firewallSyncMsg) {
	if a.resolver != nil {
		a.resolver.SetFirewallKeyError(m.provider, m.err)
	}
	if m.err != nil {
		a.addSystem("Vulnetix AI Firewall: could not store the " + m.provider + " key · " + mcpClean(m.err.Error(), 200) + " · the provider stays direct")
	} else {
		a.addSystem("Vulnetix AI Firewall: " + m.provider + " key synced (stored encrypted for your org)")
	}
	if cfg, err := a.resolveConfig(); err == nil {
		a.cfg = cfg
	}
	a.refreshFooter()
}

type cliError string

func (e cliError) Error() string { return string(e) }
