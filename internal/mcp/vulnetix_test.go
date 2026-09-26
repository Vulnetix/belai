package mcp

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
)

// vulnetixServer serves the fake MCP server over TLS and returns a client
// that dials it for any host, so a real https://mcp.vulnetix.com URL reaches it.
func vulnetixServer(t *testing.T) *http.Client {
	t.Helper()
	plain := serveHTTP(t, false)
	t.Cleanup(plain.Close)
	srv := httptest.NewTLSServer(plain.Config.Handler)
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	return &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test server
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}}
}

func TestVulnetixRefResolvesForVulnetixHost(t *testing.T) {
	hc := vulnetixServer(t)
	calls := 0
	m := Start(context.Background(), &config.MCPSettings{Servers: map[string]config.MCPServer{
		"fake": {Transport: "http", URL: "https://mcp.vulnetix.com/mcp", Headers: map[string]string{"Authorization": VulnetixCLIRef}},
	}}, Options{HTTPClient: hc, VulnetixAuth: func() (string, error) { calls++; return "Bearer tok", nil }})
	defer m.Close()
	checkEcho(t, m)
	if calls != 1 {
		t.Fatalf("credential resolved %d times, want once per dial", calls)
	}
}

func TestVulnetixRefGuard(t *testing.T) {
	auth := func() (string, error) { return "ApiKey org:secret", nil }
	m := &Manager{opts: Options{VulnetixAuth: auth}}
	for _, tc := range []struct {
		url, header string
	}{
		{"https://evil.example.com/mcp", "Authorization"},
		{"http://mcp.vulnetix.com/mcp", "Authorization"},
		{"https://mcp.vulnetix.com.evil.com/mcp", "Authorization"},
		{"https://notvulnetix.com/mcp", "Authorization"},
		{"https://user@mcp.vulnetix.com/mcp", "Authorization"},
		{"https://mcp.vulnetix.com/mcp", "X-Leak"},
	} {
		if h, err := m.resolveHeaders(tc.url, map[string]string{tc.header: VulnetixCLIRef}); err == nil {
			t.Errorf("%s %s: resolved to %v", tc.url, tc.header, h)
		}
	}
	h, err := m.resolveHeaders("https://mcp.vulnetix.com/mcp", map[string]string{"authorization": VulnetixCLIRef, "X-Other": "v"})
	if err != nil || h["authorization"] != "ApiKey org:secret" || h["X-Other"] != "v" {
		t.Fatalf("h = %v err = %v", h, err)
	}
}

func TestVulnetixRefMissingCredentialFails(t *testing.T) {
	m := Start(context.Background(), &config.MCPSettings{Servers: map[string]config.MCPServer{
		"vulnetix": {Transport: "http", URL: "https://mcp.vulnetix.com/mcp", Headers: map[string]string{"Authorization": VulnetixCLIRef}},
	}}, Options{VulnetixAuth: func() (string, error) { return "", errors.New("no Vulnetix credential found") }})
	defer m.Close()
	st := m.Status()
	if len(st) != 1 || st[0].State != StateFailed || !strings.Contains(st[0].Err, "/vulnetix setup") {
		t.Fatalf("status = %+v", st)
	}
}

func TestUpsertAndRemove(t *testing.T) {
	hc := vulnetixServer(t)
	m := Start(context.Background(), nil, Options{HTTPClient: hc, VulnetixAuth: func() (string, error) { return "Bearer tok", nil }})
	defer m.Close()
	err := m.Upsert(context.Background(), "fake", config.MCPServer{
		Transport: "http", URL: "https://mcp.vulnetix.com/mcp", Headers: map[string]string{"Authorization": VulnetixCLIRef},
	})
	if err != nil {
		t.Fatal(err)
	}
	checkEcho(t, m)
	m.Remove("fake")
	if len(m.Tools()) != 0 || len(m.Status()) != 0 {
		t.Fatal("removed server still listed")
	}
}
