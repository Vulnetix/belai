package run

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/vulnetix/belai/internal/firewall"
	"github.com/vulnetix/belai/internal/kiroauth"
	"github.com/vulnetix/belai/internal/kiromodels"
	"github.com/vulnetix/belai/internal/provider"
	"github.com/vulnetix/belai/internal/wire"
)

// TestKiroThroughVulnetixFirewallSendsTheVulnetixKey routes a Kiro turn
// through the Vulnetix AI Firewall: Belai sends its Vulnetix key as the Bearer
// token, leaves the profile to the gateway, and never mints a Kiro token.
func TestKiroThroughVulnetixFirewallSendsTheVulnetixKey(t *testing.T) {
	var oidcCalls atomic.Int32
	oidc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		oidcCalls.Add(1)
		http.Error(w, "no", http.StatusBadRequest)
	}))
	t.Cleanup(oidc.Close)
	prev := kiroRefresher
	kiroRefresher = kiroauth.NewRefresher(oidc.Client()).WithBaseURL(oidc.URL)
	t.Cleanup(func() { kiroRefresher = prev })
	kiromodels.Forget()
	t.Cleanup(kiromodels.Forget)

	var gotAuth, listAuth string
	var gotReq wire.KiroRequest
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/kiro/org-1/v1" + kiromodels.Path:
			listAuth = r.Header.Get("Authorization")
			if r.URL.Query().Get("profileArn") != "" {
				t.Errorf("the profile is the gateway's to set: %s", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, kiroTestCatalog)
		case "/kiro/org-1/v1" + wire.KiroPath:
			gotAuth = r.Header.Get("Authorization")
			_ = json.NewDecoder(r.Body).Decode(&gotReq)
			w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
			_, _ = w.Write(kiroStreamBody())
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(gw.Close)

	base := gw.URL + "/kiro/org-1/v1"
	route := &firewall.Route{Instance: "vulnetix", AdapterID: "vulnetix", Mode: "vulnetix", BaseURL: base, APIKey: "vx-key"}
	cfg := Config{Provider: "kiro", Auth: provider.AuthKiro, BaseURL: base, APIKey: "vx-key", Model: "claude-sonnet-4.5", Firewall: route}
	a, err := SendTurns(context.Background(), cfg, "", []Turn{{Role: "user", Content: "hi"}}, gw.Client())
	if err != nil {
		t.Fatalf("SendTurns: %v", err)
	}
	if a.Text != "Hello" {
		t.Fatalf("assistant = %+v", a)
	}
	if gotAuth != "Bearer vx-key" || listAuth != "Bearer vx-key" {
		t.Fatalf("Authorization: request %q, model list %q", gotAuth, listAuth)
	}
	if gotReq.ProfileArn != "" {
		t.Fatalf("profileArn = %q, want the gateway to set it", gotReq.ProfileArn)
	}
	if n := oidcCalls.Load(); n != 0 {
		t.Fatalf("Belai minted a Kiro token %d times while routed", n)
	}
}
