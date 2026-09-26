package tui

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vulnetix/belai/internal/modelfetch"
	"github.com/vulnetix/belai/internal/models"
)

var errTestTargetUnresolved = errors.New("cloudflare-ai-gateway: account_id missing")

// The footer's context meter reads the live catalogue's context window, so the
// catalogue must be warmed on startup rather than when /model first opens.
func TestPrefetchCatalogCmdResolvesTargetOffThread(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "k")

	a := New(Options{Workdir: t.TempDir()})
	a.cfg.Provider = "anthropic"

	cmd := a.prefetchCatalogCmd("anthropic")
	if cmd == nil {
		t.Fatal("prefetchCatalogCmd returned nil for a fetchable provider")
	}
	msg, ok := cmd().(catalogTargetMsg)
	if !ok {
		t.Fatalf("expected catalogTargetMsg, got %T", cmd())
	}
	if msg.provider != "anthropic" {
		t.Fatalf("provider = %q", msg.provider)
	}
	if msg.endpoint != "https://api.anthropic.com/v1/models" {
		t.Fatalf("endpoint = %q", msg.endpoint)
	}
}

// A prefetch already in flight, or an already-cached provider, must not fetch
// again when the picker opens (and vice versa).
func TestPrefetchCatalogCmdSkipsCachedAndInFlight(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})

	a.catalogLoading = map[string]bool{"anthropic": true}
	if cmd := a.prefetchCatalogCmd("anthropic"); cmd != nil {
		t.Fatal("prefetch started while a fetch was already in flight")
	}
	a.catalogLoading = nil
	a.catalogCache = map[string][]models.Model{"anthropic": nil}
	if cmd := a.prefetchCatalogCmd("anthropic"); cmd != nil {
		t.Fatal("prefetch started for an already-cached provider")
	}
	if cmd := a.prefetchCatalogCmd(""); cmd != nil {
		t.Fatal("prefetch started without a provider")
	}
}

// The resolved target runs through the normal fetch path, and the window it
// carries reaches the footer without the picker ever opening.
func TestCatalogPrefetchFillsFooterContextLimit(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{
			map[string]any{"id": "claude-test", "max_input_tokens": 123456},
		}})
	}))
	t.Cleanup(srv.Close)

	a := New(Options{Workdir: t.TempDir()})
	a.client = srv.Client()
	a.cfg.Provider = "anthropic"
	a.cfg.Model = "claude-test"

	cmd := a.handleCatalogTarget(catalogTargetMsg{
		provider: "anthropic",
		target:   modelfetch.Target{Name: "anthropic", BaseURL: srv.URL, APIKey: "k"},
		endpoint: srv.URL + "/v1/models",
	})
	if cmd == nil {
		t.Fatal("handleCatalogTarget did not start a fetch")
	}
	if !a.catalogLoading["anthropic"] {
		t.Fatal("provider not marked as loading while the prefetch is in flight")
	}
	fetched, ok := cmd().(modelsFetchedMsg)
	if !ok {
		t.Fatalf("expected modelsFetchedMsg, got %T", cmd())
	}
	if fetched.err != nil {
		t.Fatalf("fetch: %v", fetched.err)
	}
	a.handleModelsFetched(fetched)

	if a.catalogLoading["anthropic"] {
		t.Fatal("loading flag not cleared after the fetch landed")
	}
	a.refreshFooter()
	if a.footer.ContextLimit != 123456 {
		t.Fatalf("footer.ContextLimit = %d, want 123456", a.footer.ContextLimit)
	}
}

// A prefetch that cannot resolve its target stays silent: the error belongs on
// the picker, not in the transcript.
func TestCatalogPrefetchFailureIsSilent(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	before := len(a.messages)

	if cmd := a.handleCatalogTarget(catalogTargetMsg{provider: "cloudflare-ai-gateway", err: errTestTargetUnresolved}); cmd != nil {
		t.Fatal("a failed target resolution must not start a fetch")
	}
	if len(a.messages) != before {
		t.Fatalf("prefetch failure wrote %d transcript message(s)", len(a.messages)-before)
	}
	if a.catalogErr["cloudflare-ai-gateway"] == "" {
		t.Fatal("prefetch failure not recorded for the picker")
	}
}
