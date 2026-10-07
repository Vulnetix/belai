package main

import (
	"context"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/clefmcp"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/mcp"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
)

// credMap is a run.CredentialSource over fixed values.
type credMap map[string]string

func (c credMap) Lookup(provider, field string) (string, string, bool) {
	v, ok := c[provider+"/"+field]
	return v, "test", ok
}

func withCreds(t *testing.T, c run.CredentialSource) {
	t.Helper()
	old := builtinCreds
	builtinCreds = func(string) run.CredentialSource { return c }
	t.Cleanup(func() { builtinCreds = old })
}

const testAccount = "0123456789abcdef0123456789abcdef"

func start(t *testing.T, s config.Settings) *mcp.Manager {
	t.Helper()
	m := mcp.Start(context.Background(), nil, withBuiltinMCP(mcp.Options{}, s, t.TempDir()))
	t.Cleanup(m.Close)
	return m
}

func off(v bool) *bool { return &v }

// With Cloudflare Workers AI credentials the clef server is offered in any build,
// with all nine tools as read-only decision tools named mcp__clef__<tool>.
func TestClefMCPOfferedWithCloudflareCredentials(t *testing.T) {
	withCreds(t, credMap{"cloudflare-workers-ai/account_id": testAccount, "cloudflare-workers-ai/api_key": "k"})
	st := start(t, config.Settings{}).Status()
	if len(st) != 1 || st[0].Name != "clef" || st[0].State != mcp.StateRunning || st[0].Transport != mcp.BuiltinTransport || len(st[0].Tools) != 9 {
		t.Fatalf("status = %+v", st)
	}
	for _, tl := range start(t, config.Settings{}).Tools() {
		if tl.Kind() != tools.KindDecision || !tl.Kind().ReadOnly() || tl.Kind().NeedsClassifier() || tools.Mutates(tl) {
			t.Errorf("%s must be a read-only, sanitise-only decision tool, got %s", tl.Definition().Name, tl.Kind())
		}
		if !strings.Contains(tl.Definition().Description, "[Built-in decision server") {
			t.Errorf("%s description does not say it is built in: %q", tl.Definition().Name, tl.Definition().Description)
		}
	}
}

// Without credentials the server is compiled in but listed as disabled with the
// reason, except in the Pix Sandbox build, which always has a backend.
func TestClefMCPNeedsABackendOutsideTheSandbox(t *testing.T) {
	withCreds(t, nil)
	st := start(t, config.Settings{}).Status()
	if len(st) != 1 || st[0].Name != "clef" {
		t.Fatalf("status = %+v", st)
	}
	if clefmcp.SandboxBuild {
		if st[0].State != mcp.StateRunning {
			t.Fatalf("the sandbox build must offer clef: %+v", st[0])
		}
		return
	}
	if st[0].State != mcp.StateDisabled || !strings.Contains(st[0].Diag, "Cloudflare") || len(st[0].Tools) != 0 {
		t.Fatalf("status = %+v", st[0])
	}
}

// The user's switch turns it off whatever the backend.
func TestClefMCPCanBeSwitchedOff(t *testing.T) {
	withCreds(t, credMap{"cloudflare-workers-ai/account_id": testAccount, "cloudflare-workers-ai/api_key": "k"})
	s := config.Settings{MCP: &config.MCPSettings{Builtin: &config.MCPBuiltin{Clef: &config.ClefMCPSettings{Enabled: off(false)}}}}
	st := start(t, s).Status()
	if len(st) != 1 || st[0].State != mcp.StateDisabled || !strings.Contains(st[0].Diag, "switched off") {
		t.Fatalf("status = %+v", st)
	}
}

// With no decision model a call is a tool error, not a crash and not an answer.
func TestClefWithoutDecisionModelIsAToolError(t *testing.T) {
	withCreds(t, credMap{"cloudflare-workers-ai/account_id": "not-an-account", "cloudflare-workers-ai/api_key": "k"})
	m := mcp.Start(context.Background(), nil, func() mcp.Options {
		o := withBuiltinMCP(mcp.Options{}, config.Settings{}, t.TempDir())
		o.BuiltinOff = nil // force it on to exercise the call path
		return o
	}())
	defer m.Close()
	for _, tl := range m.Tools() {
		if tl.Definition().Name != "mcp__clef__decide_boolean" {
			continue
		}
		res, err := tl.Execute(context.Background(), map[string]any{"question": "Is it?"})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(res.Content, "MCP tool reported an error") {
			t.Errorf("content = %q", res.Content)
		}
		return
	}
	t.Fatal("decide_boolean not offered")
}

// The hosted Vulnetix server is added in memory while credentials exist and the
// user has not switched it off or configured one.
func TestEffectiveMCPAddsVulnetixOnlyWithCredentialsAndConsent(t *testing.T) {
	for _, k := range []string{"VULNETIX_API_KEY", "VULNETIX_ORG_ID", "VVD_ORG", "VVD_SECRET"} {
		t.Setenv(k, "") // the machine's own credentials must not decide this test
	}
	t.Setenv("VULNETIX_API_TOKEN", "tok")
	t.Setenv("VULNETIX_CREDENTIALS_DIR", t.TempDir())
	dir := t.TempDir()
	got := effectiveMCP(config.Settings{}, dir)
	if got == nil || got.Servers[config.VulnetixMCPName].URL != config.VulnetixMCPURL {
		t.Fatalf("with credentials: %+v", got)
	}
	s := config.Settings{MCP: &config.MCPSettings{Builtin: &config.MCPBuiltin{Vulnetix: &config.VulnetixMCPSettings{Enabled: off(false)}}}}
	if got := effectiveMCP(s, dir); got != nil && len(got.Servers) != 0 {
		t.Fatalf("switched off: %+v", got)
	}
	own := config.MCPServer{Transport: "http", URL: "https://example.com/mcp"}
	s = config.Settings{MCP: &config.MCPSettings{Servers: map[string]config.MCPServer{config.VulnetixMCPName: own}}}
	if got := effectiveMCP(s, dir); got.Servers[config.VulnetixMCPName].URL != own.URL {
		t.Fatalf("an explicit entry must win: %+v", got)
	}
	t.Setenv("VULNETIX_API_TOKEN", "")
	if got := effectiveMCP(config.Settings{}, dir); got != nil && len(got.Servers) != 0 {
		t.Fatalf("without credentials: %+v", got)
	}
}
