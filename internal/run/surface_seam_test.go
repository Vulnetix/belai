package run

import (
	"context"
	"net/http"
	"testing"

	"github.com/vulnetix/belai/internal/calltrace"
	"github.com/vulnetix/belai/internal/provider"
	"github.com/vulnetix/belai/internal/wire"
)

// The Workers AI route and the session-affinity header key on the
// descriptor's surface. These tests hold the two seams to the old name-keyed
// answers for every provider the build compiles in.

func TestWorkersAISurfaceDrivesDialectAndAffinity(t *testing.T) {
	ctx := calltrace.WithSession(context.Background(), "sess-1")
	for _, name := range provider.Names() {
		d, _ := provider.Lookup(name)
		onWorkers := d.Surface == wire.SurfaceWorkersAI
		got, err := resolveDialect(Config{Provider: name, Model: "m"})
		if err != nil {
			t.Fatalf("%s: resolveDialect: %v", name, err)
		}
		if isWorkers := got.kind == kindWorkersAI && got.route == routeWorkersAI; isWorkers != onWorkers {
			t.Errorf("%s: workers route = %v, surface says %v", name, isWorkers, onWorkers)
		}
		h := http.Header{}
		applySessionAffinity(ctx, Config{Provider: name, Model: "m"}, h)
		wantAffinity := onWorkers || name == "cloudflare-ai-gateway"
		if (h.Get(headerSessionAffinity) != "") != wantAffinity {
			t.Errorf("%s: affinity header = %q, want sent = %v", name, h.Get(headerSessionAffinity), wantAffinity)
		}
	}
}

func TestStockWorkersAIAndGatewayUnchanged(t *testing.T) {
	d, ok := provider.Lookup("cloudflare-workers-ai")
	if !ok || d.Surface != wire.SurfaceWorkersAI {
		t.Fatalf("cloudflare-workers-ai surface = %q", d.Surface)
	}
	if !wantsSessionAffinity(Config{Provider: "cloudflare-workers-ai"}) {
		t.Error("stock Workers AI lost session affinity")
	}
	if !wantsSessionAffinity(Config{Provider: "cloudflare-ai-gateway", Model: "gpt-5"}) {
		t.Error("gateway non-Claude lost session affinity")
	}
	if wantsSessionAffinity(Config{Provider: "cloudflare-ai-gateway", Model: "claude-sonnet-4-5"}) {
		t.Error("gateway Claude gained session affinity")
	}
	for _, name := range []string{"", "unknown-provider", "my-workers-ai"} {
		if wantsSessionAffinity(Config{Provider: name}) {
			t.Errorf("%q gained session affinity", name)
		}
	}
	// A custom API profile is not a Workers AI route even under a stock name.
	if got, err := resolveDialect(Config{Provider: "cloudflare-workers-ai", API: wire.SurfaceOpenAIChat}); err != nil || got.kind != kindOpenAIChat {
		t.Errorf("custom API dialect = %+v, %v", got, err)
	}
}

// undocumentedProviders names compiled-in providers a build variant adds that
// the public docs deliberately do not list. The doc-parity tests skip them.
var undocumentedProviders = map[string]bool{}
