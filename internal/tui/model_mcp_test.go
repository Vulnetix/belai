package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/config"
)

func noVulnetixCredentials(t *testing.T) {
	t.Helper()
	old := vulnetixAuth
	vulnetixAuth = func(string) (string, error) { return "", errors.New("no credentials") }
	t.Cleanup(func() { vulnetixAuth = old })
}

func withVulnetixCredentials(t *testing.T) {
	t.Helper()
	old := vulnetixAuth
	vulnetixAuth = func(string) (string, error) { return "ApiKey x", nil }
	t.Cleanup(func() { vulnetixAuth = old })
}

func mcpModelScreen(t *testing.T) *App {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	a := newModelScreen(t, t.TempDir())
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 50})
	return a
}

func rowOf(t *testing.T, a *App, key string) settingsRow {
	t.Helper()
	for _, r := range a.modelRows() {
		if r.role == roleMCP && r.key == key {
			return r.settingsRow
		}
	}
	t.Fatalf("no mcp row %q", key)
	return settingsRow{}
}

// The group lists the decision server and its threshold, plus the hosted
// Vulnetix server only while Vulnetix credentials exist.
func TestModelMCPGroupRows(t *testing.T) {
	noVulnetixCredentials(t)
	a := mcpModelScreen(t)
	if r := rowOf(t, a, "mcp_clef"); r.value != "on" || r.kind != "toggle" {
		t.Fatalf("clef row = %+v", r)
	}
	if r := rowOf(t, a, "mcp_skip_ask"); r.value != "0.90" || r.kind != "choose" || r.disabled {
		t.Fatalf("skip row = %+v", r)
	}
	for _, r := range a.modelRows() {
		if r.key == "mcp_vulnetix" {
			t.Fatal("the Vulnetix row needs credentials")
		}
	}
	withVulnetixCredentials(t)
	if r := rowOf(t, a, "mcp_vulnetix"); r.value != "on" {
		t.Fatalf("with credentials the Vulnetix server defaults on: %+v", r)
	}
}

// The group saves to the global file and `s` does not cycle it.
func TestModelMCPScopeIsFixed(t *testing.T) {
	noVulnetixCredentials(t)
	a := mcpModelScreen(t)
	_ = a.enterModel()
	selectRow(t, a, roleMCP, "mcp_clef")
	before := a.modelState
	_ = a.cycleScope()
	if a.modelState.agentScope != before.agentScope || a.modelState.classifierScope != before.classifierScope ||
		a.modelState.routingScope != before.routingScope {
		t.Fatalf("s changed a scope: %+v", a.modelState)
	}
	if got := a.roleScope(roleMCP); got != "global" {
		t.Fatalf("scope = %q", got)
	}
}

// Toggling and cycling write the mcp.builtin key of the global settings at once.
func TestModelMCPRowsWriteTheGlobalSettings(t *testing.T) {
	noVulnetixCredentials(t)
	a := mcpModelScreen(t)
	_ = a.enterModel()

	selectRow(t, a, roleMCP, "mcp_clef")
	_ = a.changeModelRow()
	g := settingsAt(t, config.ScopeGlobal, a.workdir)
	if g.ClefMCPEnabled() {
		t.Fatal("space on the clef row must switch it off in the global settings")
	}
	if r := rowOf(t, a, "mcp_skip_ask"); !r.disabled {
		t.Fatalf("with clef off the threshold row is greyed out: %+v", r)
	}
	_ = a.unsetModelRow()
	if g := settingsAt(t, config.ScopeGlobal, a.workdir); !g.ClefMCPEnabled() {
		t.Fatal("c must put the row back to its default (on)")
	}

	selectRow(t, a, roleMCP, "mcp_skip_ask")
	var seen []string
	for range mcpSkipAskOptions {
		_ = a.changeModelRow()
		seen = append(seen, rowOf(t, a, "mcp_skip_ask").value)
	}
	if strings.Join(seen, ",") != "0.95,0.99,off,0.70,0.80,0.85,0.90" {
		t.Fatalf("cycle = %v", seen)
	}
	// Landed back on 0.90; "off" must have written skip_ask false at its turn.
	_ = a.changeModelRow() // 0.95
	_ = a.changeModelRow() // 0.99
	_ = a.changeModelRow() // off
	if _, on := settingsAt(t, config.ScopeGlobal, a.workdir).ClefSkipAsk(); on {
		t.Fatal("off must write skip_ask false")
	}
	_ = a.changeModelRow() // 0.70
	if th, on := settingsAt(t, config.ScopeGlobal, a.workdir).ClefSkipAsk(); !on || th != 0.70 {
		t.Fatalf("0.70 must switch skip ask back on at 0.70: %v %v", th, on)
	}
}

func TestModelMCPVulnetixToggleWritesTheSwitch(t *testing.T) {
	withVulnetixCredentials(t)
	a := mcpModelScreen(t)
	_ = a.enterModel()
	selectRow(t, a, roleMCP, "mcp_vulnetix")
	_ = a.changeModelRow()
	if settingsAt(t, config.ScopeGlobal, a.workdir).VulnetixMCPEnabled() {
		t.Fatal("space must switch the Vulnetix server off in the global settings")
	}
}
