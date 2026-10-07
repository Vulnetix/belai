package rc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/libitem"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// memMCPStore records what a request tried to store, in memory.
type memMCPStore struct{ stored map[string]string }

func (m *memMCPStore) StoreMCPSecret(server, key, secret string) error {
	if secret == "" {
		panic("an empty secret must never reach the store")
	}
	m.stored[server+"/"+key] = secret
	return nil
}
func (m *memMCPStore) ClearMCPSecret(server, key string) { delete(m.stored, server+"/"+key) }
func (m *memMCPStore) HasMCPSecret(server, key string) bool {
	_, ok := m.stored[server+"/"+key]
	return ok
}

type mcpSecretsHarness struct {
	t     *testing.T
	d     *Daemon
	store *memMCPStore
	on    bool
	mu    sync.Mutex
	calls int
	query string
	body  string
}

func newMCPSecretsHarness(t *testing.T) *mcpSecretsHarness {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	h := &mcpSecretsHarness{t: t, on: true, store: &memMCPStore{stored: map[string]string{}}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		defer h.mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/hosts/"+testHost+"/library/mcp-secrets") {
			h.calls++
			h.query = r.URL.RawQuery
			w.Header().Set("Cache-Control", "no-store")
			w.Write([]byte(h.body))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	client, err := sessionsync.NewClient(srv.URL, func() (string, error) { return "ApiKey o:k", nil }, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(Options{Client: client, HostID: testHost, Exe: "/bin/belai", Out: &strings.Builder{},
		SyncItem: func(k libitem.Kind) bool { return h.on }})
	if err != nil {
		t.Fatal(err)
	}
	h.d = d
	old := openMCPStore
	openMCPStore = func() (MCPKeyStore, error) { return h.store, nil }
	t.Cleanup(func() { openMCPStore = old })
	return h
}

func (h *mcpSecretsHarness) install(server string, srv config.MCPServer) {
	h.t.Helper()
	if err := config.Mutate(config.ScopeGlobal, "", func(s *config.Settings) error {
		if s.MCP == nil {
			s.MCP = &config.MCPSettings{Servers: map[string]config.MCPServer{}}
		}
		s.MCP.Servers[server] = srv
		return nil
	}); err != nil {
		h.t.Fatal(err)
	}
}

func boundServer() config.MCPServer {
	return config.MCPServer{
		Command: "acme-mcp",
		Env:     map[string]string{"ACME_TOKEN": "cred:token", "ACME_REGION": "cred:region"},
		Secrets: map[string]string{"token": "ACME_VAULT_TOKEN", "region": "ACME_VAULT_REGION"},
	}
}

func (h *mcpSecretsHarness) request(server string, keys ...string) (string, string) {
	return h.d.installMCPSecrets(context.Background(), sessionsync.Dispatch{ID: "d-1", Kind: "mcp_secrets_install", Server: server, Keys: keys})
}

// A secret is stored under the installed server's own name (the library folds
// case), only for a key its entry binds, and neither the report nor the log holds
// a value.
func TestMCPSecretsAreStoredForTheInstalledServerAndNamedByKeyOnly(t *testing.T) {
	h := newMCPSecretsHarness(t)
	h.install("Acme", boundServer())
	h.body = `{"secrets":[{"key":"token","value":"  TOPSECRET-1  "},{"key":"region","value":"eu-west"}],"missing":[]}`
	report, why := h.request("acme", "token", "region")
	if why != "" {
		t.Fatalf("refused: %s", why)
	}
	if h.store.stored["Acme/token"] != "TOPSECRET-1" || h.store.stored["Acme/region"] != "eu-west" || len(h.store.stored) != 2 {
		t.Fatalf("stored = %v", h.store.stored)
	}
	if !strings.Contains(report, "stored secrets token, region for Acme") {
		t.Errorf("report = %q", report)
	}
	if strings.Contains(report, "TOPSECRET") || strings.Contains(report, "eu-west") {
		t.Errorf("a value reached the report: %q", report)
	}
	if h.calls != 1 || h.query != "dispatch=d-1" {
		t.Errorf("the library was asked %d times with ?%s", h.calls, h.query)
	}
}

// An empty key list means every key the entry binds; an unsent key is reported.
func TestMCPSecretsEmptyKeysMeansEveryBoundKeyAndMissingOnesAreReported(t *testing.T) {
	h := newMCPSecretsHarness(t)
	h.install("Acme", boundServer())
	h.body = `{"secrets":[{"key":"token","value":"v1"}],"missing":["region"]}`
	report, why := h.request("Acme")
	if why != "" {
		t.Fatalf("refused: %s", why)
	}
	if len(h.store.stored) != 1 || !strings.Contains(report, "region (the library holds no usable value for it)") {
		t.Fatalf("stored = %v report = %q", h.store.stored, report)
	}
}

// Everything the host can check is checked before the library is asked, so a
// request cannot make the host fetch or write what it does not hold a server for.
func TestMCPSecretsRefusalsBeforeTheLibraryIsAsked(t *testing.T) {
	h := newMCPSecretsHarness(t)
	h.install("Acme", boundServer())
	cases := map[string]struct {
		server string
		keys   []string
		want   string
	}{
		"not a server name": {"../x", nil, "not an MCP server name"},
		"not installed":     {"Other", nil, "install it first"},
		"key not bound":     {"Acme", []string{"other"}, "does not bind a credential named other"},
		"not a credential":  {"Acme", []string{"has space"}, "not a credential key"},
		"too many":          {"Acme", manyKeys(17), "does not bind"},
	}
	for name, c := range cases {
		report, why := h.request(c.server, c.keys...)
		if report != "" || !strings.Contains(why, c.want) {
			t.Errorf("%s: report %q why %q, want %q", name, report, why, c.want)
		}
	}
	if h.calls != 0 || len(h.store.stored) != 0 {
		t.Errorf("the library was asked %d times and %v stored for refused requests", h.calls, h.store.stored)
	}
	h.on = false
	if _, why := h.request("Acme"); !strings.Contains(why, "sync.mcps is off") {
		t.Errorf("with the switch off: %q", why)
	}
}

func manyKeys(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "k" + strings.Repeat("x", i)
	}
	return out
}

// A response with a secret over the limit is refused whole, and nothing is stored.
func TestMCPSecretsAnOversizedSecretRefusesTheResponse(t *testing.T) {
	h := newMCPSecretsHarness(t)
	h.install("Acme", boundServer())
	h.body = `{"secrets":[{"key":"token","value":"fine"},{"key":"region","value":"` + strings.Repeat("k", sessionsync.MaxMCPSecretBytes+1) + `"}]}`
	report, why := h.request("Acme")
	if report != "" || !strings.Contains(why, "longer than 4096 bytes") || len(h.store.stored) != 0 {
		t.Fatalf("report %q why %q stored %v", report, why, h.store.stored)
	}
}

// Nothing stored is a refusal, not a success.
func TestMCPSecretsNothingStoredIsARefusal(t *testing.T) {
	h := newMCPSecretsHarness(t)
	h.install("Acme", boundServer())
	h.body = `{"secrets":[],"missing":["token","region"]}`
	report, why := h.request("Acme")
	if report != "" || !strings.Contains(why, "no secret was stored") {
		t.Fatalf("report %q why %q", report, why)
	}
}

// A remove clears the named secrets and asks the library nothing.
func TestMCPSecretsRemoveClearsThem(t *testing.T) {
	h := newMCPSecretsHarness(t)
	h.install("Acme", boundServer())
	h.store.stored["Acme/token"], h.store.stored["Acme/region"] = "a", "b"
	report, why := h.d.removeMCPSecrets(sessionsync.Dispatch{Kind: "mcp_secrets_remove", Server: "acme", Keys: []string{"token"}})
	if why != "" || !strings.Contains(report, "cleared secrets token for Acme") {
		t.Fatalf("report %q why %q", report, why)
	}
	if h.store.HasMCPSecret("Acme", "token") || !h.store.HasMCPSecret("Acme", "region") || h.calls != 0 {
		t.Fatalf("stored = %v calls %d", h.store.stored, h.calls)
	}
}

// The secret type never prints, marshals or formats its value.
func TestMCPSecretNeverPrintsItsValue(t *testing.T) {
	s := sessionsync.MCPSecret{Key: "token"}
	for _, out := range []string{s.String(), s.GoString()} {
		if strings.Contains(out, "<redacted>") == false {
			t.Errorf("%q", out)
		}
	}
}
