package tui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/credentials"
	"github.com/vulnetix/signet/internal/wire"
)

// newTestResolver builds a resolver over a temp workdir.
func newTestResolver(t *testing.T, workdir string) *credentials.Resolver {
	t.Helper()
	res, err := credentials.NewResolver(workdir)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	return res
}

// newAvailabilityApp returns an App with a real resolver (so filtering is
// live) and a seeded availability cache, bypassing the network.
func newAvailabilityApp(t *testing.T, configured, local map[string]bool) *App {
	t.Helper()
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir, Resolver: newTestResolver(t, workdir)})
	a.avail = providerAvailability{configured: configured, local: local, probedAt: time.Now()}
	return a
}

func TestAvailableProvidersFiltersAndPins(t *testing.T) {
	a := newAvailabilityApp(t,
		map[string]bool{"openai": true, "anthropic": true, "ollama": true, "llama-server": true},
		map[string]bool{"ollama": true, "llama-server": false},
	)
	a.cfg.Provider = "openrouter" // committed but unconfigured: must survive

	got := a.availableProviders(a.cfg.Provider)

	want := map[string]bool{"openai": true, "anthropic": true, "ollama": true, "openrouter": true}
	if len(got) != len(want) {
		t.Fatalf("availableProviders = %v, want exactly %v", got, want)
	}
	for _, name := range got {
		if !want[name] {
			t.Fatalf("availableProviders offered %q; want only %v", name, want)
		}
	}

	// Canonical ordering is what the /credentials jump and the tab strip rely
	// on, so the filtered list must stay a subsequence of the full one.
	all := a.providerNames()
	j := 0
	for _, name := range got {
		for j < len(all) && all[j] != name {
			j++
		}
		if j == len(all) {
			t.Fatalf("availableProviders = %v is not in providerNames order %v", got, all)
		}
	}

	if a.providerAvailable("llama-server") {
		t.Fatal("llama-server has no server answering; it must not report available")
	}
	if a.providerAvailable("openrouter") {
		t.Fatal("openrouter is unconfigured; it must not report available")
	}
	if !a.providerAvailable("ollama") {
		t.Fatal("ollama is configured with a live server; it must report available")
	}
}

func TestAvailableProvidersNeverEmpty(t *testing.T) {
	cases := map[string]providerAvailability{
		"nothing configured": {configured: map[string]bool{}, local: map[string]bool{}, probedAt: time.Now()},
		"probe not landed":   {},
	}
	for name, avail := range cases {
		t.Run(name, func(t *testing.T) {
			a := newAvailabilityApp(t, nil, nil)
			a.avail = avail

			got := a.availableProviders("")
			if !reflect.DeepEqual(got, a.providerNames()) {
				t.Fatalf("availableProviders = %v, want the full list %v", got, a.providerNames())
			}
			if a.avail.note == "" {
				t.Fatal("falling back to the full list must explain itself in the note")
			}
			// modelView indexes into the result; an empty list would panic.
			if len(got) == 0 {
				t.Fatal("availableProviders returned an empty slice")
			}
		})
	}
}

func TestAvailableProvidersWithoutResolver(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	if a.resolver != nil {
		t.Fatal("no Resolver option should leave the resolver nil")
	}
	if got := a.availableProviders(""); !reflect.DeepEqual(got, a.providerNames()) {
		t.Fatalf("availableProviders = %v, want the full list", got)
	}
	if !a.providerAvailable("openai") {
		t.Fatal("without a resolver nothing can be ruled out")
	}
}

func TestProbeAvailabilityReachesLocalServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	cases := map[string]struct {
		host string
		want bool
	}{
		"live server":   {host: srv.URL, want: true},
		"closed port":   {host: "http://127.0.0.1:1", want: false},
		"no such route": {host: srv.URL + "/nope", want: false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// OLLAMA_HOST takes a whole base URL; the SIGNET_OLLAMA_* fields
			// take a decomposed host/port/protocol.
			t.Setenv("OLLAMA_HOST", tc.host)
			a := newAvailabilityApp(t, nil, nil)

			cmd := a.probeAvailabilityCmd()
			if cmd == nil {
				t.Fatal("probeAvailabilityCmd returned nil with a resolver present")
			}
			msg, ok := cmd().(availabilityMsg)
			if !ok {
				t.Fatalf("probe returned %T, want availabilityMsg", msg)
			}
			if !msg.configured["ollama"] {
				t.Fatal("ollama has only optional fields; it must report configured")
			}
			if got := msg.local["ollama"]; got != tc.want {
				t.Fatalf("local[ollama] = %v, want %v", got, tc.want)
			}

			a.handleAvailability(msg)
			if a.avail.probedAt.IsZero() || a.avail.inFlight {
				t.Fatalf("handleAvailability left the cache unfilled: %+v", a.avail)
			}
			if got := a.providerAvailable("ollama"); got != tc.want {
				t.Fatalf("providerAvailable(ollama) = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAvailabilityStaleness(t *testing.T) {
	a := newAvailabilityApp(t, map[string]bool{}, map[string]bool{})

	if cmd := a.availabilityCmdIfStale(); cmd != nil {
		t.Fatal("a fresh probe must not re-probe")
	}

	a.avail.probedAt = time.Now().Add(-availabilityTTL - time.Second)
	if cmd := a.availabilityCmdIfStale(); cmd == nil {
		t.Fatal("a stale probe must re-probe")
	}

	a.avail.inFlight = true
	if cmd := a.availabilityCmdIfStale(); cmd != nil {
		t.Fatal("a probe already in flight must not be started twice")
	}

	a.avail.inFlight = false
	a.invalidateAvailability()
	if !a.avail.probedAt.IsZero() {
		t.Fatal("invalidateAvailability must clear the probe timestamp")
	}
	if cmd := a.availabilityCmdIfStale(); cmd == nil {
		t.Fatal("an invalidated cache must re-probe")
	}
}

// TestCredentialMutationInvalidatesAvailability pins that storing or clearing
// a credential cannot leave a stale answer behind.
func TestCredentialMutationInvalidatesAvailability(t *testing.T) {
	a := newAvailabilityApp(t, map[string]bool{"openai": true}, map[string]bool{})
	a.refreshCredentials()
	if !a.avail.probedAt.IsZero() {
		t.Fatal("refreshCredentials must invalidate the availability cache")
	}
}

// TestProbeAvailabilityWithoutResolverIsNil keeps the probe off the Init batch
// when there is nothing to resolve.
func TestProbeAvailabilityWithoutResolverIsNil(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	var cmd tea.Cmd = a.probeAvailabilityCmd()
	if cmd != nil {
		t.Fatal("probeAvailabilityCmd must be nil without a resolver")
	}
}

func TestKeylessCustomProviderNeedsLiveness(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	cases := []struct {
		name    string
		base    string
		offered bool
	}{
		{"live endpoint", srv.URL + "/v1", true},
		{"dead endpoint", "http://127.0.0.1:1/v1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SIGNET_HOME", t.TempDir())
			workdir := t.TempDir()
			if err := config.SaveGlobal(config.Settings{
				Providers: map[string]config.ProviderProfile{
					"lite-llm": {BaseURL: tc.base, API: wire.SurfaceOpenAIChat, Kind: "openai-compatible"},
				},
			}); err != nil {
				t.Fatal(err)
			}
			a := New(Options{Workdir: workdir, Resolver: newTestResolver(t, workdir)})
			cmd := a.probeAvailabilityCmd()
			if cmd == nil {
				t.Fatal("probeAvailabilityCmd returned nil with a resolver present")
			}
			msg, ok := cmd().(availabilityMsg)
			if !ok {
				t.Fatalf("probe returned %T, want availabilityMsg", msg)
			}
			if !msg.configured["lite-llm"] {
				t.Fatal("keyless custom provider must report configured (api_key optional)")
			}
			if !msg.keyless["lite-llm"] {
				t.Fatal("lite-llm must be flagged keyless")
			}
			if msg.local["lite-llm"] != tc.offered {
				t.Fatalf("local[lite-llm] = %v, want %v", msg.local["lite-llm"], tc.offered)
			}

			a.handleAvailability(msg)
			if got := a.providerAvailable("lite-llm"); got != tc.offered {
				t.Fatalf("providerAvailable(lite-llm) = %v, want %v", got, tc.offered)
			}
		})
	}
}

// TestCredentialsResolvedSchedulesAvailabilityProbe verifies that credential
// resolution at startup immediately schedules the same availability probe that
// /providers runs, so the user does not have to open the providers screen to
// see which providers are live.
func TestCredentialsResolvedSchedulesAvailabilityProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	t.Setenv("SIGNET_HOME", t.TempDir())
	// Point the built-in local providers at the same test server so the
	// probe finishes instantly instead of timing out against localhost.
	t.Setenv("OLLAMA_HOST", srv.URL)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	t.Setenv("SIGNET_LLAMA_HOST", u.Hostname())
	t.Setenv("SIGNET_LLAMA_PORT", u.Port())
	t.Setenv("SIGNET_LLAMA_PROTOCOL", u.Scheme)

	workdir := t.TempDir()
	a := New(Options{Workdir: workdir, Resolver: newTestResolver(t, workdir)})

	cmd := a.handleCredentialsResolved(credentialsResolvedMsg{cfg: a.cfg, status: a.status})
	if cmd == nil {
		t.Fatal("handleCredentialsResolved must return a command when a resolver is present")
	}

	if !drainForMsg(t, cmd, func(m tea.Msg) bool { _, ok := m.(availabilityMsg); return ok }) {
		t.Fatal("handleCredentialsResolved did not schedule an availability probe")
	}
}

// drainForMsg executes a command (and any nested commands/messages) until it
// finds a message satisfying want. It understands bubbletea's BatchMsg so a
// single returned tea.Batch can be expanded.
func drainForMsg(t *testing.T, cmd tea.Cmd, want func(tea.Msg) bool) bool {
	t.Helper()
	return matchMsg(t, cmd(), want, 0)
}

func matchMsg(t *testing.T, m tea.Msg, want func(tea.Msg) bool, depth int) bool {
	t.Helper()
	if depth > 12 {
		t.Fatal("command nesting exceeded safe limit")
	}
	if want(m) {
		return true
	}
	// A command may return another command as its message; unwrap it.
	if f, ok := m.(tea.Cmd); ok && f != nil {
		if matchMsg(t, f(), want, depth+1) {
			return true
		}
	}
	// Expand bubbletea batch/sequence messages.
	switch v := m.(type) {
	case tea.BatchMsg:
		for _, sub := range v {
			if sub != nil && matchMsg(t, sub(), want, depth+1) {
				return true
			}
		}
	default:
		// Older API compatibility: reflect any struct field named cmds.
		rv := reflect.ValueOf(m)
		if rv.Kind() == reflect.Struct {
			f := rv.FieldByName("cmds")
			if f.IsValid() && f.Kind() == reflect.Slice {
				for i := 0; i < f.Len(); i++ {
					if sub, ok := f.Index(i).Interface().(tea.Cmd); ok && sub != nil {
						if matchMsg(t, sub(), want, depth+1) {
							return true
						}
					}
				}
			}
		}
	}
	return false
}
