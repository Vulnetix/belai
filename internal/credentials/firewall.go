package credentials

import (
	"strings"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/firewall"
	"github.com/vulnetix/belai/internal/provider"
	"github.com/vulnetix/belai/internal/vulnetixcreds"
	"github.com/vulnetix/belai/internal/wire"
)

// FirewallState describes whether the active AI Firewall can route a
// provider, independently of the enabled flag. It is the source of truth for
// honest availability messages.
type FirewallState struct {
	Instance string
	Adapter  firewall.Adapter
	Label    string
	// Routable is set when the adapter can carry this provider at all.
	Routable bool
	// HasCred is set when the firewall's own key resolved (or none is
	// needed).
	HasCred bool
	// Account is the Vulnetix organisation for the vulnetix adapter.
	Account string
	// Route is populated only when Reason is empty.
	Route  firewall.Route
	Reason string
}

// Ready reports whether the provider would be routed.
func (s FirewallState) Ready() bool { return s.Reason == "" }

// FirewallState returns the active firewall's state for provider.
func (r *Resolver) FirewallState(providerName string) FirewallState {
	return r.FirewallStateFor(r.settings.FirewallActive(), providerName)
}

// FirewallStateFor returns a named firewall instance's state for provider.
func (r *Resolver) FirewallStateFor(instance, providerName string) FirewallState {
	st := FirewallState{Instance: instance}
	inst, ok := r.settings.FirewallInstanceNamed(instance)
	if !ok {
		st.Reason = "no firewall named " + instance + " is configured"
		return st
	}
	inst = firewall.WithDefaults(inst)
	a, ok := firewall.Lookup(inst.Adapter)
	if !ok {
		st.Reason = "unknown firewall adapter " + inst.Adapter
		return st
	}
	st.Adapter, st.Label = a, a.Label()
	// Decision backends are never routed: they are not chat endpoints, and a
	// firewall key must never ride on a decision request.
	if providerName == decisions.LocalProvider || providerName == decisions.TypeSafeProvider || r.settings.Providers[providerName].Kind == decisions.JevKind {
		st.Reason = "decision backends are never routed through a firewall"
		return st
	}
	target := r.firewallTarget(providerName)
	if len(inst.Providers) > 0 && !contains(inst.Providers, providerName) {
		st.Reason = st.Label + " is limited to " + strings.Join(inst.Providers, ", ")
		return st
	}
	if ok, why := a.Supports(target); !ok {
		st.Reason = why
		return st
	}
	st.Routable = true

	var secret string
	switch {
	case firewall.CapabilitiesOf(a).VulnetixCredential:
		cred, err := r.loadVulnetixCred()
		if err != nil {
			st.Reason = err.Error()
			return st
		}
		if cred.OrgUUID == "" || cred.APIKey == "" {
			st.Reason = vulnetixcreds.ErrNoGatewayCredential.Error()
			return st
		}
		secret, st.Account = cred.APIKey, cred.OrgUUID
	case firewall.NeedsSecret(inst):
		secret = r.firewallSecret(instance)
		if secret == "" {
			st.Reason = st.Label + " has no key: add one in /firewall"
			return st
		}
	}
	st.HasCred = true

	route, err := firewall.Plan(instance, inst, target, secret, st.Account)
	if err != nil {
		st.Reason = err.Error()
		return st
	}
	st.Route = route
	if firewall.CapabilitiesOf(a).KeySync {
		if err := r.firewallKeyError(providerName); err != nil {
			st.Reason = "the " + st.Label + " has no " + providerName + " key for this org (" + err.Error() + ")"
		}
	}
	return st
}

// Firewall implements run.FirewallSource. It returns ok=true when the
// firewall is on and the active instance can route the provider.
func (r *Resolver) Firewall(providerName string) (firewall.Route, bool) {
	if !r.firewallEnabled() {
		return firewall.Route{}, false
	}
	st := r.FirewallState(providerName)
	if !st.Ready() {
		return firewall.Route{}, false
	}
	return st.Route, true
}

// FirewallEnabled reports the effective switch, override included.
func (r *Resolver) FirewallEnabled() bool { return r.firewallEnabled() }

// firewallSecret reads an instance's key from the environment
// (BELAI_FIREWALL_<NAME>_API_KEY), the user credentials file or the keychain.
// The repo-visible project credentials file and netrc are never consulted:
// a firewall key is the user's own.
func (r *Resolver) firewallSecret(instance string) string {
	name := config.FirewallCredentialName(instance)
	spec := SpecFor(name, nil)
	if v, ok := r.fromEnv(spec[0]); ok {
		return v.Reveal()
	}
	if v, ok, _ := r.userFile.read(name, "api_key", spec); ok {
		return v.Reveal()
	}
	if r.keychainAvailable() {
		if v, err := r.keychain.Get(name + ":api_key"); err == nil {
			return v
		}
	}
	return ""
}

// HasFirewallSecret reports whether an instance's key resolves.
func (r *Resolver) HasFirewallSecret(instance string) bool {
	return r.firewallSecret(instance) != ""
}

// StoreFirewallSecret writes an instance's key to the preferred backend
// (keychain, else the user credentials file).
func (r *Resolver) StoreFirewallSecret(instance, secret string) error {
	return r.Store(config.FirewallCredentialName(instance), "api_key", secret, r.PreferredBackend())
}

// ClearFirewallSecret removes an instance's key from every writable user
// backend.
func (r *Resolver) ClearFirewallSecret(instance string) {
	name := config.FirewallCredentialName(instance)
	_ = r.userFile.delete(name, "api_key")
	if r.keychainAvailable() {
		_ = r.keychain.Delete(name + ":api_key")
	}
}

// firewallTarget describes a provider for firewall routing.
func (r *Resolver) firewallTarget(providerName string) firewall.Target {
	t := firewall.Target{Provider: providerName}
	if d, ok := provider.Lookup(providerName); ok {
		t.Surface, t.Auth, t.BaseURL = d.Surface, d.Auth, d.BaseURL
		if d.BaseURLField != "" {
			if v, _, ok := r.Lookup(providerName, d.BaseURLField); ok && v != "" {
				t.BaseURL = v
			}
		}
		return t
	}
	if p, ok := r.settings.Providers[providerName]; ok {
		t.Surface, t.Auth, t.BaseURL = p.API, provider.Auth(p.Auth), p.BaseURL
		if tpl, ok := provider.Template(p.Kind); ok {
			if t.Surface == "" {
				t.Surface = tpl.Surface
			}
			if t.BaseURL == "" {
				t.BaseURL = tpl.BaseURL
			}
		}
		if t.Surface == "" {
			t.Surface = wire.SurfaceOpenAIChat
		}
		if t.Auth == "" {
			t.Auth = provider.AuthBearer
		}
	}
	return t
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
