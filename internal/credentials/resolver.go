package credentials

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/vulnetix/signet/internal/aifirewall"
	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/provider"
	"github.com/vulnetix/signet/internal/vulnetixcreds"
)

// Resolver resolves provider credentials from a stack of sources.
type Resolver struct {
	env      func(string) string
	workdir  string
	home     string
	settings config.Settings
	userFile *fileStore
	projFile *fileStore
	netrc    *netrcStore
	keychain Keychain

	// keychainAvail memoises the keychain availability probe. Available()
	// performs a real DBus/Secret-Service round trip with a 5s timeout, and
	// Resolve/Lookup/Backends all used to re-probe it per field per provider.
	// The answer is constant for a Resolver's lifetime, so it is cached.
	keychainAvail    bool
	keychainAvailSet bool

	// vulnetixCreds are loaded on demand because they may probe a
	// service-scoped keyring; the result is cached for the resolver lifetime.
	vulnetixCredOnce sync.Once
	vulnetixCred     vulnetixcreds.Credential
	vulnetixCredErr  error
	vulnetixKeychain Keychain

	// firewallEnabledOverride lets the TUI toggle the firewall flag for this
	// process without rewriting the settings file. nil means use settings.
	firewallEnabledOverride *bool
}

// keychainAvailable reports whether the host keychain is reachable, probing
// once and caching the result.
func (r *Resolver) keychainAvailable() bool {
	if !r.keychainAvailSet {
		r.keychainAvailSet = true
		r.keychainAvail = r.keychain.Available()
	}
	return r.keychainAvail
}

// BackendInfo describes one credential backend.
type BackendInfo struct {
	Name      string `json:"name"`
	Writable  bool   `json:"writable"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// ConfiguredProviders returns every provider for which at least one field
// resolves through this resolver: built-ins first, then configured custom
// names.
func (r *Resolver) ConfiguredProviders() []string {
	var out []string
	for _, p := range r.providerNames() {
		if r.Configured(p) {
			out = append(out, p)
		}
	}
	return out
}

// providerNames returns built-in providers followed by configured custom names,
// sorted within the custom group.
func (r *Resolver) providerNames() []string {
	names := append([]string{}, provider.Names()...)
	var custom []string
	for name := range r.settings.Providers {
		if !provider.Builtin(name) {
			custom = append(custom, name)
		}
	}
	sort.Strings(custom)
	return append(names, custom...)
}

// Profile returns the domain provider profile for a configured custom name.
func (r *Resolver) Profile(name string) (provider.Profile, bool) {
	p, ok := r.settings.Providers[name]
	if !ok {
		return provider.Profile{}, false
	}
	auth := provider.AuthBearer
	if p.Auth != "" {
		auth = provider.Auth(p.Auth)
	}
	models := make([]string, 0, len(p.Models))
	for _, m := range p.Models {
		models = append(models, m.ID)
	}
	return provider.Profile{BaseURL: p.BaseURL, API: p.API, Auth: auth, Models: models, Kind: p.Kind}, true
}

// CanonicalProvider implements run.AliasSource: it resolves a user-facing
// display label back to the canonical provider slug.
func (r *Resolver) CanonicalProvider(label string) (string, bool) {
	return r.settings.CanonicalProvider(label)
}

// Label returns the user-facing display label for a provider name.
func (r *Resolver) Label(name string) string {
	return r.settings.LabelFor(name)
}

// spec returns the required fields for a provider, prepending a configured
// profile's api_key_env so it is preferred over the derived variable. A
// kind'd profile's spec follows the template, so a local instance offers an
// optional api_key rather than a mandatory one.
func (r *Resolver) spec(provider string) []Field {
	var prof *config.ProviderProfile
	if p, ok := r.settings.Providers[provider]; ok {
		prof = &p
	}
	spec := SpecFor(provider, prof)
	if prof != nil && prof.APIKeyEnv != "" {
		for i := range spec {
			if spec[i].Name == "api_key" {
				spec[i].EnvVars = append([]string{prof.APIKeyEnv}, spec[i].EnvVars...)
			}
		}
	}
	return spec
}

// Configured reports whether the given provider has every required field
// resolved through this resolver.
func (r *Resolver) Configured(provider string) bool {
	return r.Resolve(provider).Complete()
}

// NewResolver builds a resolver for the given working directory.
// It reads from the environment, project file, user file, netrc,
// and host keychain in that order.
func NewResolver(workdir string) (*Resolver, error) {
	userPath, err := config.UserCredentialsPath()
	if err != nil {
		return nil, err
	}
	projPath := config.ProjectCredentialsPath(workdir)
	settings, err := config.LoadMerged(workdir)
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return &Resolver{
		env:              os.Getenv,
		workdir:          workdir,
		home:             home,
		settings:         settings,
		userFile:         newFileStore(userPath, false),
		projFile:         newFileStore(projPath, true),
		netrc:            newNetrcStore(),
		keychain:         newKeyringBackend(),
		vulnetixKeychain: NewKeyringBackend("vulnetix"),
	}, nil
}

// loadVulnetixCred resolves the Vulnetix gateway credential once.
func (r *Resolver) loadVulnetixCred() (vulnetixcreds.Credential, error) {
	r.vulnetixCredOnce.Do(func() {
		r.vulnetixCred, r.vulnetixCredErr = vulnetixcreds.Load(r.env, r.home, r.workdir, r.vulnetixKeychain)
	})
	return r.vulnetixCred, r.vulnetixCredErr
}

// FirewallState describes why the AI Firewall can or cannot route a provider
// independently of the enabled flag.
type FirewallState struct {
	Routable bool // provider has a gateway slug
	Slug     string
	HasCred  bool // a gateway credential resolved
	OrgUUID  string
	Gateway  string // resolved gateway host
	BaseURL  string // populated only when Routable && HasCred
	APIKey   string
	Reason   string // empty when Routable && HasCred
}

// FirewallState returns the reasoned firewall availability for provider without
// consulting the firewall enabled flag. It is the source of truth for honest
// availability messages.
func (r *Resolver) FirewallState(provider string) FirewallState {
	st := FirewallState{}
	slug, ok := aifirewall.Slug(provider)
	st.Routable = ok
	st.Slug = slug
	if !ok {
		st.Reason = fmt.Sprintf("provider %q cannot be routed through the gateway — routable providers: %s", provider, strings.Join(aifirewall.Providers(), ", "))
		return st
	}
	cred, err := r.loadVulnetixCred()
	if err != nil {
		st.Reason = err.Error()
		return st
	}
	if cred.OrgUUID == "" || cred.APIKey == "" {
		errNoCred := vulnetixcreds.ErrNoGatewayCredential
		if errNoCred != nil {
			st.Reason = errNoCred.Error()
		} else {
			st.Reason = "no Vulnetix credential found"
		}
		return st
	}
	st.HasCred = true
	st.OrgUUID = cred.OrgUUID
	st.APIKey = cred.APIKey
	st.Gateway = aifirewall.DefaultGateway
	if r.settings.Vulnetix != nil {
		st.Gateway = r.settings.Vulnetix.GatewayURLOrDefault()
	}
	st.BaseURL = aifirewall.BaseURL(st.Gateway, slug, cred.OrgUUID)
	return st
}

// Firewall implements run.FirewallSource. It returns ok=true when the firewall
// setting is on, a usable Vulnetix credential exists, and the provider can be
// routed through the gateway.
func (r *Resolver) Firewall(provider string) (baseURL, apiKey string, ok bool) {
	if !r.firewallEnabled() {
		return "", "", false
	}
	st := r.FirewallState(provider)
	if st.Reason != "" {
		return "", "", false
	}
	return st.BaseURL, st.APIKey, true
}

// firewallEnabled reads the override first, then the settings file.
func (r *Resolver) firewallEnabled() bool {
	if r.firewallEnabledOverride != nil {
		return *r.firewallEnabledOverride
	}
	return r.settings.FirewallEnabled()
}

// SetSettings replaces the resolver's settings snapshot after a live edit.
func (r *Resolver) SetSettings(s config.Settings) {
	r.settings = s
}

// SetFirewallEnabled overrides the settings-file firewall flag. Pass nil to
// restore the settings-file value.
func (r *Resolver) SetFirewallEnabled(on *bool) {
	r.firewallEnabledOverride = on
}

// RefreshVulnetixCred resets the cached Vulnetix credential so the next
// Firewall/FirewallState call re-reads it from disk/keyring.
func (r *Resolver) RefreshVulnetixCred() {
	r.vulnetixCredOnce = sync.Once{}
}

// Resolve returns a Set containing every known value and every missing field.
func (r *Resolver) Resolve(provider string) Set {
	spec := r.spec(provider)
	set := Set{
		Provider: provider,
		Values:   map[string]Value{},
	}
	resolved := map[string]bool{}

	// The netrc file is read once up front: its contents do not change during
	// one Resolve, and a single read keeps the world-readable warning
	// de-duplicated and race-free when Resolve runs concurrently.
	netrcValues, netrcNotes, _ := r.netrc.read(providerHost(provider))

	for _, f := range spec {
		if resolved[f.Name] {
			continue
		}
		// 1. Environment
		if v, ok := r.fromEnv(f); ok {
			set.Values[f.Name] = v
			resolved[f.Name] = true
			continue
		}
		// 2. Project file
		if v, ok, note := r.projFile.read(provider, f.Name, spec); ok {
			set.Values[f.Name] = v
			resolved[f.Name] = true
			continue
		} else if note != "" {
			set.Notes = append(set.Notes, note)
		}
		// 3. User file
		if v, ok, note := r.userFile.read(provider, f.Name, spec); ok {
			set.Values[f.Name] = v
			resolved[f.Name] = true
			continue
		} else if note != "" {
			set.Notes = append(set.Notes, note)
		}
		// 4. Netrc
		if val, ok := netrcValues[f.Name]; ok {
			set.Values[f.Name] = Value{
				Field: f.Name, Location: "~/.netrc", Source: SourceNetrc, Secret: f.Secret,
				value: val,
			}
			resolved[f.Name] = true
			continue
		}
		// 5. Keychain
		if r.keychainAvailable() {
			account := provider + ":" + f.Name
			val, err := r.keychain.Get(account)
			if err == nil {
				set.Values[f.Name] = Value{
					Field: f.Name, Location: "keychain", Source: SourceKeychain, Secret: f.Secret,
					value: val,
				}
				resolved[f.Name] = true
				continue
			}
		}
		if !f.Optional {
			set.Missing = append(set.Missing, f.Name)
		}
	}
	if len(netrcNotes) > 0 {
		set.Notes = append(set.Notes, netrcNotes...)
	}
	return set
}

// Lookup returns the value and origin for a single field.
func (r *Resolver) Lookup(provider, field string) (value, origin string, ok bool) {
	spec := r.spec(provider)
	// Environment first.
	for _, f := range spec {
		if f.Name != field {
			continue
		}
		for _, ev := range f.EnvVars {
			if v := r.env(ev); v != "" {
				return v, fmt.Sprintf("env $%s", ev), true
			}
		}
		break
	}
	// Project file.
	if v, ok, _ := r.projFile.read(provider, field, spec); ok {
		return v.Reveal(), v.Location, true
	}
	// User file.
	if v, ok, _ := r.userFile.read(provider, field, spec); ok {
		return v.Reveal(), v.Location, true
	}
	// Netrc.
	if m, _, ok := r.netrc.read(providerHost(provider)); ok {
		if val, ok := m[field]; ok {
			return val, "~/.netrc", true
		}
	}
	// Keychain.
	if r.keychainAvailable() {
		account := provider + ":" + field
		val, err := r.keychain.Get(account)
		if err == nil {
			return val, "keychain", true
		}
	}
	return "", "", false
}

// Store writes a credential to the chosen backend.
func (r *Resolver) Store(provider, field, secret string, backend Source) error {
	switch backend {
	case SourceKeychain:
		if !r.keychainAvailable() {
			return fmt.Errorf("keychain is not available")
		}
		return r.keychain.Set(provider+":"+field, secret)
	case SourceUserFile:
		return r.userFile.write(provider, field, secret)
	case SourceProjectFile:
		return r.projFile.write(provider, field, secret)
	default:
		return fmt.Errorf("backend %q does not support writes", backend)
	}
}

// StoreEnvRef stores a credential as the name of an environment variable
// rather than a value. Only file backends can hold a reference; a keychain
// entry cannot.
func (r *Resolver) StoreEnvRef(provider, field, envName string, backend Source) error {
	if !config.ValidEnvName(envName) {
		return fmt.Errorf("invalid env var name %q", envName)
	}
	switch backend {
	case SourceUserFile:
		return r.userFile.writeEnvRef(provider, field, envName)
	case SourceProjectFile:
		return r.projFile.writeEnvRef(provider, field, envName)
	default:
		return fmt.Errorf("backend %q cannot hold an env reference", backend)
	}
}

// Clear removes a credential from the chosen backend.
func (r *Resolver) Clear(provider, field string, backend Source) error {
	switch backend {
	case SourceKeychain:
		if !r.keychainAvailable() {
			return fmt.Errorf("keychain is not available")
		}
		return r.keychain.Delete(provider + ":" + field)
	case SourceUserFile:
		return r.userFile.delete(provider, field)
	case SourceProjectFile:
		return r.projFile.delete(provider, field)
	default:
		return fmt.Errorf("backend %q does not support deletes", backend)
	}
}

// Backends returns the status of every backend.
func (r *Resolver) Backends() []BackendInfo {
	var out []BackendInfo
	out = append(out, BackendInfo{Name: "env", Writable: false, Available: true})

	projAvail := r.projFile.exists()
	out = append(out, BackendInfo{Name: "project-file", Writable: true, Available: projAvail})

	userAvail := r.userFile.exists()
	out = append(out, BackendInfo{Name: "user-file", Writable: true, Available: userAvail})

	out = append(out, BackendInfo{Name: "netrc", Writable: false, Available: r.netrc.exists()})

	kcAvail := r.keychainAvailable()
	reason := ""
	if !kcAvail {
		if kb, ok := r.keychain.(*keyringBackend); ok {
			reason = kb.reasonText()
		} else {
			reason = "not available"
		}
	}
	out = append(out, BackendInfo{Name: "keychain", Writable: true, Available: kcAvail, Reason: reason})

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r *Resolver) fromEnv(f Field) (Value, bool) {
	for _, ev := range f.EnvVars {
		if v := r.env(ev); v != "" {
			return Value{
				Field: f.Name, Location: "$" + ev, Source: SourceEnv, Secret: f.Secret,
				value: v,
			}, true
		}
	}
	return Value{}, false
}
