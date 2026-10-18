package run

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vulnetix/belai/internal/firewall"
	"github.com/vulnetix/belai/internal/nonce"
)

// The nonce-mode header goes out only on a Vulnetix route whose gateway
// issued every nonce the pool holds: canonical when that gateway advertised
// it, enforce otherwise.
func TestApplyFirewallNonceMode(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(nonce.NonceResponse{Nonces: []string{"n1"}})
	}))
	defer gw.Close()
	remote := nonce.New()
	if _, err := remote.SeedFromEndpoints(gw.Client(), []nonce.Endpoint{nonce.BearerEndpoint("vulnetix", gw.URL, "k")}, 1); err != nil {
		t.Fatal(err)
	}
	cgw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(nonce.NonceResponse{Nonces: []string{"n1"}, Modes: []string{"observe", "enforce", "canonical"}})
	}))
	defer cgw.Close()
	canon := nonce.New()
	if _, err := canon.SeedFromEndpoints(cgw.Client(), []nonce.Endpoint{nonce.BearerEndpoint("vulnetix", cgw.URL, "k")}, 1); err != nil {
		t.Fatal(err)
	}
	local := nonce.New()
	_ = local.Seed(1)

	vx := &firewall.Route{Instance: "vulnetix", AdapterID: "vulnetix"}
	cases := []struct {
		name string
		cfg  Config
		pool *nonce.Pool
		want string
	}{
		{"remote pool from this gateway", Config{BaseURL: gw.URL, Firewall: vx}, remote, "enforce"},
		{"gateway advertising canonical", Config{BaseURL: cgw.URL, Firewall: vx}, canon, "canonical"},
		{"local pool", Config{BaseURL: gw.URL, Firewall: vx}, local, ""},
		{"remote pool from elsewhere", Config{BaseURL: "https://other.example", Firewall: vx}, remote, ""},
		{"custom firewall", Config{BaseURL: gw.URL, Firewall: &firewall.Route{AdapterID: "custom"}}, remote, ""},
		{"direct", Config{BaseURL: gw.URL}, remote, ""},
	}
	for _, tc := range cases {
		h := http.Header{}
		applyFirewall(withNoncePool(context.Background(), tc.pool), tc.cfg, h)
		if got := h.Get(firewall.HeaderNonceMode); got != tc.want {
			t.Errorf("%s: nonce mode = %q, want %q", tc.name, got, tc.want)
		}
	}
}
