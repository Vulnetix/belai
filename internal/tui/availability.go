package tui

import (
	"context"
	"os"
	"sort"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/credentials"
	"github.com/vulnetix/signet/internal/localinfer"
	"github.com/vulnetix/signet/internal/provider"
	"github.com/vulnetix/signet/internal/run"
)

// localProviders returns the providers whose availability means a running
// server rather than a stored credential: every built-in with Descriptor.Local
// plus every configured profile whose kind template is local.
func localProviders(s config.Settings) []string {
	set := map[string]bool{}
	for _, name := range provider.Names() {
		if d, ok := provider.Lookup(name); ok && d.Local {
			set[name] = true
		}
	}
	for name, p := range s.Providers {
		if provider.Builtin(name) {
			continue
		}
		if d, ok := provider.Template(p.Kind); ok && d.Local {
			set[name] = true
		}
	}
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// availabilityTTL bounds how stale a probe may be before a picker re-probes.
// A server can start or stop between visits, so the answer is cached, not
// frozen.
const availabilityTTL = 30 * time.Second

// availabilityProbeTimeout bounds the whole probe. The local pings are the
// only network work in it, and a dead endpoint must not hold the cache
// in-flight for longer than a user waits before reopening a picker.
const availabilityProbeTimeout = 5 * time.Second

// providerAvailability caches which providers the pickers may offer. It is
// filled by probeAvailabilityCmd off the Update loop and read synchronously
// while rendering.
//
// The filter it feeds is a UX affordance, not a security boundary: run.Prepare
// and credentials.Resolve remain the fail-closed gate on actually using a
// provider. It therefore degrades open — when the cache cannot tell, every
// provider is offered rather than none.
type providerAvailability struct {
	configured map[string]bool // credentials fully resolved
	local      map[string]bool // local server answered GET /v1/models
	probedAt   time.Time
	inFlight   bool
	note       string // why the list fell back to everything, if it did
}

// availabilityMsg carries one completed probe back into Update.
type availabilityMsg struct {
	configured map[string]bool
	local      map[string]bool
}

// isLocalProvider reports whether a provider's availability is a running
// server rather than a stored credential.
func (a *App) isLocalProvider(name string) bool {
	for _, p := range localProviders(a.settings) {
		if p == name {
			return true
		}
	}
	return false
}

// availableProviders returns the providers a picker may offer: those whose
// credentials resolve, minus the local providers whose server is not
// answering. Every pinned name — the committed agent provider, the committed
// classifier provider — is kept even when unavailable, so a picker can never
// silently move the user off their own model.
//
// It never returns an empty slice: modelView indexes into the result, and an
// empty list would leave the user no way back to a working provider.
func (a *App) availableProviders(pinned ...string) []string {
	all := a.providerNames()

	// No resolver (the non-interactive construction path and most tests)
	// means nothing can be told about credentials at all.
	if a.resolver == nil {
		a.avail.note = ""
		return all
	}
	if a.avail.probedAt.IsZero() {
		a.avail.note = "checking providers…"
		return all
	}

	keep := map[string]bool{}
	for _, p := range pinned {
		if p != "" {
			keep[p] = true
		}
	}

	out := make([]string, 0, len(all))
	for _, name := range all {
		switch {
		case keep[name]:
		case !a.avail.configured[name]:
			continue
		case a.isLocalProvider(name) && !a.avail.local[name]:
			continue
		}
		out = append(out, name)
	}
	if len(out) == 0 {
		a.avail.note = "no configured providers — showing all; press c to add credentials"
		return all
	}
	a.avail.note = ""
	return out
}

// providerAvailable reports whether one provider is usable right now. It is
// how a pinned-but-dead entry earns its marker in the picker.
func (a *App) providerAvailable(name string) bool {
	if a.resolver == nil || a.avail.probedAt.IsZero() {
		return true
	}
	if !a.avail.configured[name] {
		return false
	}
	return !a.isLocalProvider(name) || a.avail.local[name]
}

// probeAvailabilityCmd resolves credential completeness and pings the local
// inference endpoints off the Update loop. Nothing inside the returned command
// touches *App: the resolver and the provider list are captured first.
//
// The local ping is a bare GET /v1/models carrying no credentials and no
// content, to a host the user already configured for inference.
func (a *App) probeAvailabilityCmd() tea.Cmd {
	res := a.resolver
	if res == nil {
		return nil
	}
	a.avail.inFlight = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), availabilityProbeTimeout)
		defer cancel()

		configured := map[string]bool{}
		for _, name := range res.ConfiguredProviders() {
			configured[name] = true
		}

		// Resolve the base URLs first, on this goroutine. Resolver.Lookup
		// fills lazy caches (netrc, keychain availability) without a lock, so
		// two concurrent run.Prepare calls race on them. Resolution is local
		// and cheap; the dial is what is worth parallelising.
		bases := map[string]string{}
		for _, name := range localProviders(a.settings) {
			if !configured[name] {
				continue
			}
			if cfg, _ := run.Prepare("", name, credentialSourceOf(res)); cfg.BaseURL != "" {
				bases[name] = cfg.BaseURL
			}
		}

		// Probe the local endpoints concurrently: a dead one costs a dial
		// timeout, and serialising them would double the wait.
		local := map[string]bool{}
		var mu sync.Mutex
		var wg sync.WaitGroup
		for name, base := range bases {
			wg.Add(1)
			go func(name, base string) {
				defer wg.Done()
				running := localinfer.ProbeRunning(ctx, []string{base}) != ""
				mu.Lock()
				local[name] = running
				mu.Unlock()
			}(name, base)
		}
		wg.Wait()

		return availabilityMsg{configured: configured, local: local}
	}
}

// credentialSourceOf adapts a resolver to the credential source run.Prepare
// expects, keeping the nil case explicit at one site. A nil resolver means
// environment-only resolution, matching the rest of the TUI's fallback.
func credentialSourceOf(res *credentials.Resolver) run.CredentialSource {
	if res == nil {
		return run.EnvSource(os.Getenv)
	}
	return res
}

// handleAvailability installs a completed probe.
func (a *App) handleAvailability(m availabilityMsg) tea.Cmd {
	a.avail.configured = m.configured
	a.avail.local = m.local
	a.avail.probedAt = time.Now()
	a.avail.inFlight = false
	return nil
}

// availabilityCmdIfStale returns a probe command when the cache is older than
// availabilityTTL and none is already running, else nil.
func (a *App) availabilityCmdIfStale() tea.Cmd {
	if a.resolver == nil || a.avail.inFlight {
		return nil
	}
	if !a.avail.probedAt.IsZero() && time.Since(a.avail.probedAt) < availabilityTTL {
		return nil
	}
	return a.probeAvailabilityCmd()
}

// invalidateAvailability forces the next picker entry to re-probe. Any
// credential mutation changes the answer, so every one of them calls it.
func (a *App) invalidateAvailability() {
	a.avail.probedAt = time.Time{}
}

// refreshCredentials invalidates the availability cache after credentials are
// imported or edited.
func (a *App) refreshCredentials() {
	a.invalidateAvailability()
}
