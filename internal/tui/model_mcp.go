package tui

import (
	"context"
	"fmt"
	"strconv"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/clefmcp"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/credentials"
	"github.com/vulnetix/belai/internal/mcp"
	"github.com/vulnetix/belai/internal/run"
)

// roleMCP is the /model group for the servers Belai offers itself: the clef
// decision server (on or off, and how confident it must be to answer an ask in
// your place) and the hosted Vulnetix server. The switches live under the
// mcp.builtin key of the global settings, so the group saves there and `s`
// does not cycle it.
const roleMCP modelRole = "mcp"

// mcpSkipAskOptions are the confidences the skip-ask row cycles through; off
// asks you every time.
var mcpSkipAskOptions = []string{"off", "0.70", "0.80", "0.85", "0.90", "0.95", "0.99"}

// clefEngineFor builds the decision engine over this App's current settings and
// credentials, for the status line and for re-checking availability. (A session
// gets its own, built from its snapshot: askDeciderFor.)
func (a *App) clefEngineFor() *clefmcp.Engine {
	s, src := a.settings, credentialSourceOf(a.resolver)
	return &clefmcp.Engine{
		Settings: func() config.Settings { return s },
		Creds:    func() run.CredentialSource { return src },
	}
}

// vulnetixAuth reads the Vulnetix credential; a test replaces it so the machine's
// own login never decides a result.
var vulnetixAuth = credentials.VulnetixAuthHeader

// vulnetixCredOK reports whether Vulnetix credentials exist.
func (a *App) vulnetixCredOK() bool {
	_, err := vulnetixAuth(a.workdir)
	return err == nil
}

// mcpSkipAskValue renders the skip-ask setting as one of mcpSkipAskOptions (or
// the hand-written number).
func mcpSkipAskValue(s config.Settings) string {
	th, on := s.ClefSkipAsk()
	if !on {
		return "off"
	}
	return strconv.FormatFloat(th, 'f', 2, 64)
}

// mcpRows are the rows of the built-in MCP group.
func (a *App) mcpRows() []modelRow {
	src := sourceLabel(a.eff.Origin["mcp"])
	clefOn := a.settings.ClefMCPEnabled()
	clef := settingsRow{
		key: "mcp_clef", label: "clef decider", kind: "toggle", value: boolLabel(clefOn), src: src,
		help: "decision tools the model can call (mcp__clef__*), and answers to asks when it is confident",
	}
	if ok, why := a.clefEngineFor().Offered(); clefOn {
		if ok {
			clef.note = "offered · backend: " + a.clefBackendLabel()
		} else {
			clef.note = "not offered: " + why
		}
	}
	skip := settingsRow{
		key: "mcp_skip_ask", label: "skip ask at threshold", kind: "choose", opts: mcpSkipAskOptions,
		value: mcpSkipAskValue(a.settings), src: src, disabled: !clefOn,
		help: "when clef is at least this confident its answer replaces the question to you; off asks you every time",
	}
	rows := []modelRow{{roleMCP, clef}, {roleMCP, skip}}
	if a.vulnetixCredOK() {
		rows = append(rows, modelRow{roleMCP, settingsRow{
			key: "mcp_vulnetix", label: "Vulnetix MCP", kind: "toggle", value: boolLabel(a.settings.VulnetixMCPEnabled()), src: src,
			help: "the hosted Vulnetix MCP server, signed in with your Vulnetix credentials",
		}})
	}
	return rows
}

// clefBackendLabel names where decisions are answered.
func (a *App) clefBackendLabel() string {
	if clefmcp.SandboxBuild {
		return "the sandbox Worker"
	}
	return "Cloudflare Workers AI"
}

// mcpBuiltin returns the settings' built-in block, creating it.
func mcpBuiltin(s *config.Settings) *config.MCPBuiltin {
	if s.MCP == nil {
		s.MCP = &config.MCPSettings{}
	}
	if s.MCP.Builtin == nil {
		s.MCP.Builtin = &config.MCPBuiltin{}
	}
	return s.MCP.Builtin
}

func mcpClef(s *config.Settings) *config.ClefMCPSettings {
	b := mcpBuiltin(s)
	if b.Clef == nil {
		b.Clef = &config.ClefMCPSettings{}
	}
	return b.Clef
}

func mcpVulnetix(s *config.Settings) *config.VulnetixMCPSettings {
	b := mcpBuiltin(s)
	if b.Vulnetix == nil {
		b.Vulnetix = &config.VulnetixMCPSettings{}
	}
	return b.Vulnetix
}

// changeMCPRow flips or cycles one row of the built-in MCP group and writes it
// at once: these knobs pick no model, so there is nothing to test first.
func (a *App) changeMCPRow(key string) tea.Cmd {
	var say string
	var err error
	switch key {
	case "mcp_clef":
		next := !a.settings.ClefMCPEnabled()
		err = a.mutateGlobalSetting(func(s *config.Settings) { mcpClef(s).Enabled = &next })
		say = "clef decider: " + boolLabel(next)
	case "mcp_skip_ask":
		cur := mcpSkipAskValue(a.settings)
		next := mcpSkipAskOptions[(indexOfString(mcpSkipAskOptions, cur)+1)%len(mcpSkipAskOptions)]
		err = a.mutateGlobalSetting(func(s *config.Settings) {
			c := mcpClef(s)
			if next == "off" {
				off := false
				c.SkipAsk = &off
				return
			}
			on := true
			at, _ := strconv.ParseFloat(next, 64)
			c.SkipAsk, c.SkipAskAt = &on, &at
		})
		say = "skip ask at threshold: " + next
	case "mcp_vulnetix":
		next := !a.settings.VulnetixMCPEnabled()
		err = a.mutateGlobalSetting(func(s *config.Settings) { mcpVulnetix(s).Enabled = &next })
		say = "Vulnetix MCP: " + boolLabel(next)
	default:
		return nil
	}
	if err != nil {
		a.addSystem("built-in MCP: " + err.Error())
		return nil
	}
	a.applyBuiltinMCP()
	a.addSystem(say)
	return nil
}

// unsetMCPRow puts one row back to its default.
func (a *App) unsetMCPRow(key string) tea.Cmd {
	var err error
	switch key {
	case "mcp_clef":
		err = a.mutateGlobalSetting(func(s *config.Settings) { mcpClef(s).Enabled = nil })
	case "mcp_skip_ask":
		err = a.mutateGlobalSetting(func(s *config.Settings) { c := mcpClef(s); c.SkipAsk, c.SkipAskAt = nil, nil })
	case "mcp_vulnetix":
		err = a.mutateGlobalSetting(func(s *config.Settings) { mcpVulnetix(s).Enabled = nil })
	default:
		return nil
	}
	if err != nil {
		a.addSystem("built-in MCP: " + err.Error())
		return nil
	}
	a.applyBuiltinMCP()
	return nil
}

// applyBuiltinMCP makes the settings just written take effect in this process:
// the clef server is switched on or off for its availability, the hosted
// Vulnetix server is added or removed, and the next session built sees the
// tools and the ask policy.
func (a *App) applyBuiltinMCP() {
	if m := mcp.Active(); m != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		reason := ""
		if ok, why := a.clefEngineFor().Offered(); !ok {
			reason = why
		}
		if err := m.SetBuiltinOff(ctx, clefmcp.Name, reason); err != nil {
			a.addSystem("clef MCP: " + err.Error())
		}
		a.syncVulnetixMCP(ctx, m)
	}
	a.invalidateAgentSession()
}

// syncVulnetixMCP adds the hosted Vulnetix server while its switch is on,
// credentials exist and the user has no entry of that name, and removes it when
// the switch is off.
func (a *App) syncVulnetixMCP(ctx context.Context, m *mcp.Manager) {
	own := false
	if a.settings.MCP != nil {
		_, own = a.settings.MCP.Servers[config.VulnetixMCPName]
	}
	switch {
	case !a.settings.VulnetixMCPEnabled():
		m.Remove(config.VulnetixMCPName)
	case own:
		// The user's own entry is managed by /mcp and /vulnetix mcp.
	case a.vulnetixCredOK():
		if err := m.Upsert(ctx, config.VulnetixMCPName, config.VulnetixMCPServer()); err != nil {
			a.addSystem(fmt.Sprintf("Vulnetix MCP: %v", err))
		}
	}
}
