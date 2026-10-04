package sessionsync

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/vaultenv"
)

func init() { vaultenv.SkipProtect = true }

type fakeVault struct {
	mu      sync.Mutex
	env     string
	status  int
	scrubs  []map[string]int
	scrubAt string
}

func (f *fakeVault) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		path := strings.TrimPrefix(r.URL.Path, apiPath)
		switch {
		case r.Method == http.MethodGet && path == "/hosts/h1/vault/env":
			if f.status != 0 {
				w.WriteHeader(f.status)
				return
			}
			_, _ = w.Write([]byte(f.env))
		case r.Method == http.MethodPost && path == "/hosts/h1/vault/scrub":
			f.scrubAt = path
			b, _ := io.ReadAll(r.Body)
			var in struct {
				Hits map[string]int `json:"hits"`
			}
			if err := json.Unmarshal(b, &in); err != nil {
				t.Error(err)
			}
			if strings.Contains(string(b), "value") {
				t.Errorf("a scrub report must carry names and counts only: %s", b)
			}
			f.scrubs = append(f.scrubs, in.Hits)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, path)
		}
	}
}

func newVaultClient(t *testing.T, f *fakeVault) (*Client, func()) {
	srv := httptest.NewServer(f.handler(t))
	c, err := NewClient(BaseURL(srv.URL), func() (string, error) { return "ApiKey o:k", nil }, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return c, srv.Close
}

func TestVaultEnvReadsVariablesAndTheLease(t *testing.T) {
	exp := time.Now().Add(time.Hour).UnixMilli()
	f := &fakeVault{env: `{"env":[{"name":"NPM_TOKEN","value":"npm-token-value-1"},{"name":"DB_URL","value":"postgres://u:p@h/db","expiresAt":` + vaultItoa(exp) + `}],"leaseSeconds":600}`}
	c, stop := newVaultClient(t, f)
	defer stop()
	vars, lease, err := c.VaultEnv(context.Background(), "h1")
	if err != nil || len(vars) != 2 || lease != 10*time.Minute {
		t.Fatalf("%v %v %v", vars, lease, err)
	}
	if vars[0].Name != "NPM_TOKEN" || vars[1].ExpiresAt.UnixMilli() != exp {
		t.Fatalf("%+v", vars)
	}
}

func TestVaultEnvRefusesAnOversizedLease(t *testing.T) {
	f := &fakeVault{env: `{"env":[{"name":"BIG","value":"` + strings.Repeat("x", maxVaultEnvBytes+1) + `"}],"leaseSeconds":900}`}
	c, stop := newVaultClient(t, f)
	defer stop()
	if _, _, err := c.VaultEnv(context.Background(), "h1"); err == nil {
		t.Fatal("a lease over the cap must be refused")
	}
}

func TestRenewVaultReplacesTheLeaseAndReportsScrubCounts(t *testing.T) {
	defer vaultenv.Default.Replace(nil, time.Time{})
	f := &fakeVault{env: `{"env":[{"name":"NPM_TOKEN","value":"npm-token-value-1"}],"leaseSeconds":900}`}
	c, stop := newVaultClient(t, f)
	defer stop()
	s := &Syncer{opts: Options{Client: c, HostID: "h1"}}
	_ = vaultenv.Default.Hits()
	if !s.renewVault(context.Background(), c, "h1") {
		t.Fatal("keep going")
	}
	if got := vaultenv.Default.Environ(time.Now()); len(got) != 1 || got[0] != "NPM_TOKEN=npm-token-value-1" {
		t.Fatalf("environ %v", got)
	}
	vaultenv.Default.Scrub("saw npm-token-value-1 in output")
	s.renewVault(context.Background(), c, "h1")
	if len(f.scrubs) != 1 || f.scrubs[0]["NPM_TOKEN"] != 1 {
		t.Fatalf("scrub report %v", f.scrubs)
	}
}

func TestRevokedVariableStopsBeingInjectedAtTheNextRenewal(t *testing.T) {
	defer vaultenv.Default.Replace(nil, time.Time{})
	f := &fakeVault{env: `{"env":[{"name":"NPM_TOKEN","value":"npm-token-value-1"}],"leaseSeconds":900}`}
	c, stop := newVaultClient(t, f)
	defer stop()
	s := &Syncer{opts: Options{Client: c, HostID: "h1"}}
	s.renewVault(context.Background(), c, "h1")
	f.mu.Lock()
	f.env = `{"env":[],"leaseSeconds":900}`
	f.mu.Unlock()
	s.renewVault(context.Background(), c, "h1")
	if got := vaultenv.Default.Environ(time.Now()); len(got) != 0 {
		t.Fatalf("a revoked variable must be dropped: %v", got)
	}
}

func TestAServerErrorKeepsTheCurrentLease(t *testing.T) {
	defer vaultenv.Default.Replace(nil, time.Time{})
	f := &fakeVault{env: `{"env":[{"name":"NPM_TOKEN","value":"npm-token-value-1"}],"leaseSeconds":900}`}
	c, stop := newVaultClient(t, f)
	defer stop()
	s := &Syncer{opts: Options{Client: c, HostID: "h1"}}
	s.renewVault(context.Background(), c, "h1")
	f.mu.Lock()
	f.status = http.StatusBadGateway
	f.mu.Unlock()
	if !s.renewVault(context.Background(), c, "h1") {
		t.Fatal("a failure is retried, not given up on")
	}
	if len(vaultenv.Default.Environ(time.Now())) != 1 {
		t.Fatal("the lease already held runs out on its own")
	}
}

func TestAnOlderServerEndsTheLeaseLoop(t *testing.T) {
	f := &fakeVault{status: http.StatusNotFound}
	c, stop := newVaultClient(t, f)
	defer stop()
	s := &Syncer{opts: Options{Client: c, HostID: "h1"}}
	if s.renewVault(context.Background(), c, "h1") {
		t.Fatal("a server with no vault route has nothing to lease")
	}
}

func vaultItoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
