//go:build belai_sandbox

package run

import (
	"context"
	"net/http"
	"testing"

	"github.com/vulnetix/belai/internal/calltrace"
	"github.com/vulnetix/belai/internal/wire"
)

func TestSandboxBuiltinResolvesToWorkersAIRoute(t *testing.T) {
	cfg, err := Resolve("", "builtin", envMap(map[string]string{
		"CLOUDFLARE_API_KEY":    "k",
		"CLOUDFLARE_ACCOUNT_ID": "acct123",
	}))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Model != "pix-smart" || cfg.BaseURL != "https://api.cloudflare.com/client/v4/accounts/acct123" {
		t.Fatalf("cfg = %+v", cfg)
	}
	d, err := resolveDialect(cfg)
	if err != nil {
		t.Fatalf("resolveDialect: %v", err)
	}
	if d != (dialect{kind: kindWorkersAI, route: routeWorkersAI, method: wire.ToolMethodObject}) {
		t.Fatalf("dialect = %+v", d)
	}
	h := http.Header{}
	applySessionAffinity(calltrace.WithSession(context.Background(), "sess-1"), cfg, h)
	if h.Get(headerSessionAffinity) != "sess-1" {
		t.Fatalf("affinity = %q", h.Get(headerSessionAffinity))
	}
}

func init() { undocumentedProviders["builtin"] = true }
