package main

import (
	"errors"
	"os"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/clefmcp"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/credentials"
	"github.com/vulnetix/belai/internal/jsonrpc"
	"github.com/vulnetix/belai/internal/mcp"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/vaultenv"
)

// vaultEntry returns one entry of the host's Secrets Vault lease (a Pix Sandbox
// holds one), for a vault:NAME header of an http MCP server.
func vaultEntry(name string) (string, bool) {
	prefix := name + "="
	for _, kv := range vaultenv.Default.Environ(time.Now()) {
		if v, ok := strings.CutPrefix(kv, prefix); ok {
			return v, true
		}
	}
	return "", false
}

// The one built-in MCP server, "clef", asks the decision model for a boolean, an
// enum pick, weights or an ordering (internal/clefmcp). Every build carries it;
// whether it is offered depends on the user's switch and on a decision backend
// (the Pix Sandbox Worker, or the user's own Cloudflare Workers AI credentials),
// so a build without either lists it as disabled with the reason.

// The engine answers asks in the agent's place (agent.AskDecider) as well as
// serving the MCP tools.
var _ agent.AskDecider = (*clefmcp.Engine)(nil)

// cliAskDecider is the engine that answers asks in the user's place in a headless
// or remote session, only where someone can be asked and the user has not turned
// the feature off. nil asks every time.
func cliAskDecider(canAsk bool, settings config.Settings, workdir string) agent.AskDecider {
	if !canAsk {
		return nil
	}
	eng := clefEngine(settings, workdir)
	if _, on := eng.AskPolicy(); !on {
		return nil
	}
	return eng
}

// clefEngine builds the decision engine for settings. Its decision backend is
// found on each call: the settings are re-read from the user's layers (falling
// back to the ones the process started with) and the credentials are looked up
// afresh, so a login or a /model change takes effect without a restart.
func clefEngine(settings config.Settings, workdir string) *clefmcp.Engine {
	return &clefmcp.Engine{
		Settings: func() config.Settings {
			if eff, err := config.Resolve(workdir, os.Getenv, config.Settings{}); err == nil {
				return eff.Settings
			}
			return settings
		},
		Creds: func() run.CredentialSource { return builtinCreds(workdir) },
	}
}

// builtinCreds is the credential source the decision engine reads. A test
// replaces it so the machine's own keychain is never consulted.
var builtinCreds = func(workdir string) run.CredentialSource {
	if r, err := newResolver(workdir); err == nil && r != nil {
		return r
	}
	return nil
}

// withBuiltinMCP adds the built-in servers to the options for mcp.StartAsync.
func withBuiltinMCP(o mcp.Options, settings config.Settings, workdir string) mcp.Options {
	eng := clefEngine(settings, workdir)
	// The start-up check reads the settings the process has, not a fresh file.
	start := &clefmcp.Engine{Settings: func() config.Settings { return settings }, Creds: eng.Creds}
	o.Secret = func(server, key string) (string, error) {
		r, err := newResolver(workdir)
		if err != nil || r == nil {
			return "", errors.New("credentials are not available")
		}
		return r.MCPSecret(server, key)
	}
	o.Vault = vaultEntry
	o.Builtins = map[string]jsonrpc.Handler{clefmcp.Name: eng.Handler()}
	o.DecisionBuiltins = map[string]bool{clefmcp.Name: true}
	if ok, why := start.Offered(); !ok {
		o.BuiltinOff = map[string]string{clefmcp.Name: why}
	}
	return o
}

// effectiveMCP is the mcp block the manager starts: the user's servers, plus the
// hosted Vulnetix server while Vulnetix credentials exist, the user has not
// switched it off (mcp.builtin.vulnetix.enabled) and has not configured a
// server of that name. The addition is in memory only; nothing is written.
func effectiveMCP(settings config.Settings, workdir string) *config.MCPSettings {
	m := settings.MCP
	if !settings.VulnetixMCPEnabled() {
		// Switched off: the name is the user's to turn off, including an entry
		// of their own.
		if m == nil {
			return nil
		}
		out := &config.MCPSettings{Builtin: m.Builtin, Servers: map[string]config.MCPServer{}}
		for k, v := range m.Servers {
			if k != config.VulnetixMCPName {
				out.Servers[k] = v
			}
		}
		return out
	}
	if m != nil {
		if _, ok := m.Servers[config.VulnetixMCPName]; ok {
			return m
		}
	}
	if _, err := credentials.VulnetixAuthHeader(workdir); err != nil {
		return m
	}
	out := &config.MCPSettings{Servers: map[string]config.MCPServer{}}
	if m != nil {
		out.Builtin = m.Builtin
		for k, v := range m.Servers {
			out.Servers[k] = v
		}
	}
	out.Servers[config.VulnetixMCPName] = config.VulnetixMCPServer()
	return out
}
