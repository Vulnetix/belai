package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/config"
)

func mcpScreen(t *testing.T) *App {
	t.Helper()
	noVulnetixCredentials(t)
	_, wd := isolate(t)
	a := New(Options{Workdir: wd})
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 50})
	return a
}

func mcpKey(a *App, k string) {
	switch k {
	case "enter":
		a.handleMCPKey(tea.KeyMsg{Type: tea.KeyEnter})
	case " ":
		a.handleMCPKey(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")})
	default:
		a.handleMCPKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
	}
}

func mcpSelect(t *testing.T, a *App, kind, server string) {
	t.Helper()
	for i, r := range a.mcpRowsList() {
		if r.kind == kind && r.server == server {
			a.mcpState.selected = i
			return
		}
	}
	t.Fatalf("no %s row for %q", kind, server)
}

func mcpFill(a *App, kv map[string]string) {
	for i := range a.mcpState.form.fields {
		if v, ok := kv[a.mcpState.form.fields[i].key]; ok {
			a.mcpState.form.fields[i].value = v
		}
	}
}

// Bare /mcp opens the screen; list and restart keep their text forms.
func TestMCPCommandOpensTheManagementScreen(t *testing.T) {
	a := mcpScreen(t)
	_ = a.mcpCommand("")
	if a.view != viewMCP {
		t.Fatalf("view = %v, want the MCP screen", a.view)
	}
	view := a.mcpView()
	for _, want := range []string{"MCP servers", "Built-in", "clef decider", "+ Add a server"} {
		if !strings.Contains(view, want) {
			t.Errorf("screen lacks %q:\n%s", want, view)
		}
	}
	a.pop()
	_ = a.mcpCommand("list")
	if a.view == viewMCP {
		t.Fatal("/mcp list must print, not open the screen")
	}
}

// A server is added from the form: it is saved to the global settings with a
// credential reference and no secret value, and shows under Local.
func TestMCPScreenAddsALocalServer(t *testing.T) {
	a := mcpScreen(t)
	_ = a.push(viewMCP)
	mcpSelect(t, a, "add", "")
	mcpKey(a, "enter")
	if a.mcpState.mode != "form" {
		t.Fatal("enter on Add must open the form")
	}
	mcpFill(a, map[string]string{
		mcpFieldName: "github", mcpFieldCommand: "github-mcp-server", mcpFieldArgs: "stdio",
		mcpFieldPairs: "GITHUB_PERSONAL_ACCESS_TOKEN=cred:token, LOG=debug", mcpFieldTools: "get_issue, list_issues",
	})
	a.handleMCPFormKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	if a.mcpState.errorMsg != "" {
		t.Fatalf("commit: %s", a.mcpState.errorMsg)
	}
	srv := settingsAt(t, config.ScopeGlobal, a.workdir).MCP.Servers["github"]
	if srv.Command != "github-mcp-server" || strings.Join(srv.Args, " ") != "stdio" ||
		srv.Env["GITHUB_PERSONAL_ACCESS_TOKEN"] != "cred:token" || srv.Env["LOG"] != "debug" ||
		strings.Join(srv.Tools, ",") != "get_issue,list_issues" {
		t.Fatalf("saved = %+v", srv)
	}
	found := false
	for _, r := range a.mcpRowsList() {
		found = found || (r.kind == "server" && r.server == "github")
	}
	if !found {
		t.Fatal("the new server must be listed")
	}
}

// The form refuses what the library would: a literal secret, a plain http URL, a
// duplicate name and a secret with no place to store it.
func TestMCPScreenRefusesUnsafeServers(t *testing.T) {
	cases := map[string]struct {
		fields map[string]string
		want   string
	}{
		"literal secret": {map[string]string{mcpFieldName: "a", mcpFieldCommand: "c", mcpFieldPairs: "API_TOKEN=abc123"}, "looks like a secret"},
		"plain http":     {map[string]string{mcpFieldName: "a", mcpFieldTransport: "http", mcpFieldURL: "http://example.com/mcp"}, "https, or loopback"},
		"bad pair":       {map[string]string{mcpFieldName: "a", mcpFieldCommand: "c", mcpFieldPairs: "novalue"}, "NAME=value"},
		"reserved":       {map[string]string{mcpFieldName: "clef", mcpFieldCommand: "c"}, "built-in"},
		"secret no key":  {map[string]string{mcpFieldName: "a", mcpFieldCommand: "c", mcpFieldSecret: "s3cr3t"}, "secret key"},
		"no store":       {map[string]string{mcpFieldName: "a", mcpFieldCommand: "c", mcpFieldSecretKey: "t", mcpFieldSecret: "s3cr3t"}, "credential store"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			a := mcpScreen(t)
			_ = a.push(viewMCP)
			a.mcpFormFor("", true)
			mcpFill(a, tc.fields)
			a.handleMCPFormKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
			if !strings.Contains(a.mcpState.errorMsg, tc.want) || a.mcpState.mode != "form" {
				t.Fatalf("error = %q mode = %q, want %q and the form to stay open", a.mcpState.errorMsg, a.mcpState.mode, tc.want)
			}
			if g := settingsAt(t, config.ScopeGlobal, a.workdir); g.MCP != nil && len(g.MCP.Servers) > 0 {
				t.Fatalf("nothing may be saved: %+v", g.MCP.Servers)
			}
		})
	}
}

// Space switches a server off and on; x asks twice before it deletes.
func TestMCPScreenTogglesAndDeletes(t *testing.T) {
	a := mcpScreen(t)
	_ = a.push(viewMCP)
	if err := a.mutateGlobalSetting(func(s *config.Settings) {
		s.MCP = &config.MCPSettings{Servers: map[string]config.MCPServer{
			"docs": {Transport: "http", URL: "https://mcp.example.com/mcp", Headers: map[string]string{"Authorization": "cred:token"}},
			"fs":   {Command: "fs-server"},
		}}
	}); err != nil {
		t.Fatal(err)
	}
	// Grouped: fs is local, docs is remote.
	var order []string
	for _, r := range a.mcpRowsList() {
		if r.kind == "server" {
			order = append(order, r.server)
		}
	}
	if strings.Join(order, ",") != "fs,docs" {
		t.Fatalf("local servers list before remote ones: %v", order)
	}

	mcpSelect(t, a, "server", "docs")
	mcpKey(a, " ")
	if !settingsAt(t, config.ScopeGlobal, a.workdir).MCP.Servers["docs"].Disabled {
		t.Fatal("space must disable the server")
	}
	mcpKey(a, " ")
	if settingsAt(t, config.ScopeGlobal, a.workdir).MCP.Servers["docs"].Disabled {
		t.Fatal("space again must enable it")
	}

	mcpKey(a, "x")
	if a.mcpState.confirm != "docs" {
		t.Fatal("the first x only asks")
	}
	if _, ok := settingsAt(t, config.ScopeGlobal, a.workdir).MCP.Servers["docs"]; !ok {
		t.Fatal("nothing may be deleted by the first x")
	}
	mcpKey(a, "x")
	if _, ok := settingsAt(t, config.ScopeGlobal, a.workdir).MCP.Servers["docs"]; ok {
		t.Fatal("the second x must delete the server")
	}
	if _, ok := settingsAt(t, config.ScopeGlobal, a.workdir).MCP.Servers["fs"]; !ok {
		t.Fatal("other servers stay")
	}
}

// The built-in rows on this screen are the /model group's own.
func TestMCPScreenBuiltinRowsSwitchTheSettings(t *testing.T) {
	a := mcpScreen(t)
	_ = a.push(viewMCP)
	for i, r := range a.mcpRowsList() {
		if r.kind == "builtin" && r.row.key == "mcp_clef" {
			a.mcpState.selected = i
		}
	}
	mcpKey(a, "enter")
	if settingsAt(t, config.ScopeGlobal, a.workdir).ClefMCPEnabled() {
		t.Fatal("enter on the clef row must switch it off")
	}
}

func TestParseMCPPairs(t *testing.T) {
	got, err := parseMCPPairs(" A=env:X , B = cred:k ,, ")
	if err != nil || got["A"] != "env:X" || got["B"] != "cred:k" || len(got) != 2 {
		t.Fatalf("got %v %v", got, err)
	}
	if m, err := parseMCPPairs(""); m != nil || err != nil {
		t.Fatalf("empty = %v %v", m, err)
	}
	if _, err := parseMCPPairs("=v"); err == nil {
		t.Fatal("a pair needs a name")
	}
	if formatMCPPairs(got) != "A=env:X, B=cred:k" {
		t.Fatalf("format = %q", formatMCPPairs(got))
	}
}
