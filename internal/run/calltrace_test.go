package run

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/calltrace"
	"github.com/vulnetix/belai/internal/version"
)

// TestProviderRequestCarriesTraceHeaders pins that a provider round trip
// stamps the session id, client version and a traceparent, and — being no
// tool call — no tool headers.
func TestProviderRequestCarriesTraceHeaders(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()

	cfg := Config{Provider: "openai", BaseURL: srv.URL, APIKey: "sk", Model: "gpt-5"}
	ctx := calltrace.WithSession(context.Background(), "sess-xyz")
	if _, err := Run(ctx, cfg, "ping", srv.Client()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Get(calltrace.HeaderSessionID) != "sess-xyz" {
		t.Errorf("session header = %q", got.Get(calltrace.HeaderSessionID))
	}
	if got.Get(calltrace.HeaderClientVersion) != version.Version {
		t.Errorf("client version = %q", got.Get(calltrace.HeaderClientVersion))
	}
	if got.Get("User-Agent") != version.UserAgent() {
		t.Errorf("user-agent = %q", got.Get("User-Agent"))
	}
	if !strings.HasPrefix(got.Get(calltrace.HeaderTraceparent), "00-") {
		t.Errorf("traceparent = %q", got.Get(calltrace.HeaderTraceparent))
	}
	if got.Get(calltrace.HeaderTool) != "" {
		t.Errorf("tool header on a provider request: %q", got.Get(calltrace.HeaderTool))
	}
}
