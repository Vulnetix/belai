package config

import (
	"strings"
	"testing"
)

func fptr(v float64) *float64 { return &v }
func bptr(v bool) *bool       { return &v }

func TestClefMCPDefaults(t *testing.T) {
	var s Settings
	if !s.ClefMCPEnabled() || !s.VulnetixMCPEnabled() {
		t.Fatal("both built-in servers default on")
	}
	if th, on := s.ClefSkipAsk(); !on || th != 0.90 {
		t.Fatalf("skip ask = %v %v, want on at 0.90", th, on)
	}
}

func TestClefMCPSwitches(t *testing.T) {
	s := Settings{MCP: &MCPSettings{Builtin: &MCPBuiltin{Clef: &ClefMCPSettings{SkipAskAt: fptr(0.95)}}}}
	if th, on := s.ClefSkipAsk(); !on || th != 0.95 {
		t.Fatalf("skip ask = %v %v", th, on)
	}
	s.MCP.Builtin.Clef.SkipAsk = bptr(false)
	if _, on := s.ClefSkipAsk(); on {
		t.Fatal("skip_ask false must turn decisions on asks off")
	}
	s.MCP.Builtin.Clef.SkipAsk = nil
	s.MCP.Builtin.Clef.Enabled = bptr(false)
	if s.ClefMCPEnabled() {
		t.Fatal("enabled false must switch the server off")
	}
	if _, on := s.ClefSkipAsk(); on {
		t.Fatal("a server that is off answers no asks")
	}
	s.MCP.Builtin.Vulnetix = &VulnetixMCPSettings{Enabled: bptr(false)}
	if s.VulnetixMCPEnabled() {
		t.Fatal("vulnetix enabled false must switch it off")
	}
}

func TestValidateMCPSkipAskAt(t *testing.T) {
	for _, tc := range []struct {
		at float64
		ok bool
	}{{0.9, true}, {1, true}, {0.51, true}, {0.5, false}, {0.2, false}, {1.01, false}} {
		s := Settings{MCP: &MCPSettings{Builtin: &MCPBuiltin{Clef: &ClefMCPSettings{SkipAskAt: fptr(tc.at)}}}}
		if err := ValidateMCP(s); (err == nil) != tc.ok {
			t.Errorf("skip_ask_at %v: err = %v, want ok=%v", tc.at, err, tc.ok)
		}
	}
	// A server entry written by hand never stops Belai starting.
	if err := ValidateMCP(Settings{MCP: &MCPSettings{Servers: map[string]MCPServer{"bad name!": {}}}}); err != nil {
		t.Fatalf("a hand-written entry must still load: %v", err)
	}
}

func TestMergeMCPBuiltinKeepsEarlierFields(t *testing.T) {
	a := &MCPBuiltin{Clef: &ClefMCPSettings{SkipAskAt: fptr(0.8)}}
	b := &MCPBuiltin{Clef: &ClefMCPSettings{Enabled: bptr(false)}, Vulnetix: &VulnetixMCPSettings{Enabled: bptr(false)}}
	m := mergeMCPBuiltin(a, b)
	if *m.Clef.SkipAskAt != 0.8 || *m.Clef.Enabled || *m.Vulnetix.Enabled {
		t.Fatalf("merge = %+v %+v", m.Clef, m.Vulnetix)
	}
	if a.Clef.Enabled != nil {
		t.Fatal("the earlier layer must not be modified")
	}
}

func TestParseMCPRef(t *testing.T) {
	for _, tc := range []struct {
		in   string
		kind MCPRefKind
		name string
		ok   bool
	}{
		{"plain", MCPRefNone, "", true},
		{"env:GITHUB_TOKEN", MCPRefEnv, "GITHUB_TOKEN", true},
		{"cred:api_key", MCPRefCred, "api_key", true},
		{"vault:PROD_TOKEN", MCPRefVault, "PROD_TOKEN", true},
		{"vulnetix:cli", MCPRefVulnetix, "", true},
		{"env:", MCPRefEnv, "", false},
		{"cred:has space", MCPRefCred, "has space", false},
		{"vault:1BAD", MCPRefVault, "1BAD", false},
	} {
		kind, name, ok := ParseMCPRef(tc.in)
		if kind != tc.kind || name != tc.name || ok != tc.ok {
			t.Errorf("ParseMCPRef(%q) = %q %q %v", tc.in, kind, name, ok)
		}
	}
}

func TestValidateMCPServerStrict(t *testing.T) {
	good := MCPServer{Command: "srv", Env: map[string]string{"GITHUB_TOKEN": "cred:token", "LOG": "debug"}, Secrets: map[string]string{"token": "GH_PROD"}}
	remote := MCPServer{Transport: "http", URL: "https://mcp.example.com/mcp", Headers: map[string]string{"Authorization": "vault:API_TOKEN", "X-Region": "eu"}}
	vx := MCPServer{Transport: "http", URL: VulnetixMCPURL, Headers: map[string]string{"Authorization": VulnetixCLIRef}}
	for name, s := range map[string]MCPServer{"good": good, "remote": remote, "vulnetix": vx,
		"loopback": {Transport: "http", URL: "http://127.0.0.1:9000/mcp"}} {
		if err := ValidateMCPServer("ok", s, true); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	bad := map[string]struct {
		name string
		s    MCPServer
		want string
	}{
		"name":           {"has space", good, "must be letters"},
		"reserved":       {"clef", good, "built-in"},
		"no command":     {"x", MCPServer{}, "needs a command"},
		"literal secret": {"x", MCPServer{Command: "c", Env: map[string]string{"API_TOKEN": "abc"}}, "looks like a secret"},
		"vault in env":   {"x", MCPServer{Command: "c", Env: map[string]string{"A": "vault:B"}}, "http headers only"},
		"plain http":     {"x", MCPServer{Transport: "http", URL: "http://example.com/mcp"}, "https, or loopback"},
		"userinfo":       {"x", MCPServer{Transport: "http", URL: "https://u:p@example.com/mcp"}, "credentials"},
		"literal header": {"x", MCPServer{Transport: "http", URL: "https://e.com/m", Headers: map[string]string{"Authorization": "Bearer x"}}, "looks like a secret"},
		"vx elsewhere":   {"x", MCPServer{Transport: "http", URL: "https://e.com/m", Headers: map[string]string{"Authorization": VulnetixCLIRef}}, "vulnetix.com"},
		"mixed":          {"x", MCPServer{Command: "c", URL: "https://e.com"}, "belong to an http server"},
		"dead binding":   {"x", MCPServer{Command: "c", Secrets: map[string]string{"k": "V"}}, "no cred:k value"},
		"timeout":        {"x", MCPServer{Command: "c", TimeoutMS: 700000}, "timeout_ms"},
		"bad ref":        {"x", MCPServer{Command: "c", Env: map[string]string{"A": "cred:"}}, "legal reference"},
	}
	for name, tc := range bad {
		err := ValidateMCPServer(tc.name, tc.s, true)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
	// Strictness is for forms and the library: a hand-written literal still loads.
	if err := ValidateMCPServer("x", MCPServer{Command: "c", Env: map[string]string{"API_TOKEN": "abc"}}, false); err != nil {
		t.Errorf("lenient: %v", err)
	}
}
