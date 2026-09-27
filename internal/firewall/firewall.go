// Package firewall routes model traffic through an AI Firewall and reads
// back what the firewall did. A firewall is an adapter: Vulnetix AI Firewall
// is the stable one; Fastly AI Runtime Control, Kong AI Gateway and the
// self-hosted AI Security Gateway are beta adapters built from their public
// documentation; a custom firewall is any proxy the user names. OpenRouter
// and Cloudflare AI Gateway have provider-native guardrails configured in
// their own dashboards: they are read-only here, never route anything, and
// only contribute response inspection.
//
// An adapter does two things. Route turns a configured instance and a
// provider into the base URL and headers a request uses, so a firewall key is
// only ever sent to its own instance's URL in its own header. Inspect turns a
// response's status, headers and (error or blocking) body into a Verdict: a
// small set of harness-parsed, cleaned and capped facts that the TUI renders
// as a card. A Verdict never reaches a model turn.
package firewall

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/provider"
	"github.com/vulnetix/belai/internal/wire"
)

// Stability says how far an adapter is trusted to work.
type Stability string

const (
	// Stable adapters are tested end to end.
	Stable Stability = "stable"
	// Beta adapters are built from public documentation and not yet tested
	// against the real product.
	Beta Stability = "beta"
	// Native entries are a provider's own guardrails: read-only, configured
	// in that provider's dashboard.
	Native Stability = "native"
)

// Target is the provider a route is planned for.
type Target struct {
	Provider string
	Surface  wire.Surface
	Auth     provider.Auth
	// BaseURL is the provider's own base URL, kept on the route so a
	// transparent firewall's nonce probe can fall back to it.
	BaseURL string
}

// RouteInput is what an adapter needs to plan a route.
type RouteInput struct {
	Name     string
	Instance config.FirewallInstance // defaults applied
	Target   Target
	// Secret is the firewall's own key: the instance secret from the
	// credentials resolver, or the Vulnetix API key for the vulnetix adapter.
	Secret string
	// Account is the Vulnetix organisation UUID; empty for other adapters.
	Account string
}

// Route is how one request reaches a firewall.
type Route struct {
	Instance  string
	AdapterID string
	Label     string
	Mode      string
	BaseURL   string
	// KeepProviderKey is set in transparent mode: the provider's own
	// credential and auth header are sent as normal.
	KeepProviderKey bool
	// APIKey replaces the provider key in the provider's own auth style
	// (Vulnetix). Empty when KeepProviderKey is set.
	APIKey string
	// KeyHeader and KeyValue carry the firewall key explicitly.
	KeyHeader string
	KeyValue  string
	// DropProviderAuth removes every provider auth header before KeyHeader is
	// set: in BYOK modes the firewall holds the provider key.
	DropProviderAuth bool
	// Headers are extra fixed request headers (e.g. aisg's x-provider).
	Headers map[string]string
	// UpstreamBaseURL is the provider's own base URL.
	UpstreamBaseURL string
}

// String never prints a key.
func (r Route) String() string {
	return fmt.Sprintf("firewall.Route{%s via %s (%s) %s}", r.Instance, r.AdapterID, r.Mode, r.BaseURL)
}

// GoString never prints a key.
func (r Route) GoString() string { return r.String() }

// providerAuthHeaders are the headers a provider's credential travels in.
var providerAuthHeaders = []string{"authorization", "x-api-key", "cf-aig-authorization"}

// Apply sets the route's headers on an outgoing request.
func (r *Route) Apply(h http.Header) {
	if r == nil {
		return
	}
	if r.DropProviderAuth {
		for _, k := range providerAuthHeaders {
			h.Del(k)
		}
	}
	if r.KeyHeader != "" && r.KeyValue != "" {
		h.Set(r.KeyHeader, r.KeyValue)
	}
	for k, v := range r.Headers {
		h.Set(k, v)
	}
}

// Adapter is one kind of firewall.
type Adapter interface {
	ID() string
	Label() string
	Stability() Stability
	// Summary is one line for the configuration screen.
	Summary() string
	// Defaults are the adapter's default URL, mode and header.
	Defaults() config.FirewallInstance
	// Modes are the modes the adapter accepts; empty for vulnetix and native.
	Modes() []string
	// Supports reports whether the adapter can carry a provider's traffic.
	Supports(t Target) (bool, string)
	// Route plans a request. Native adapters always return an error.
	Route(in RouteInput) (Route, error)
	// Inspect reads a response. nil means nothing worth a card.
	Inspect(in InspectInput) *Verdict
	// Instructions are static harness text for the configuration screen.
	Instructions() []string
}

// Capabilities are optional adapter behaviours.
type Capabilities struct {
	// VulnetixCredential: the key is the Vulnetix CLI's API key, not an
	// instance secret.
	VulnetixCredential bool
	// KeySync: provider keys are pushed to the firewall (BYOK) with the
	// Vulnetix CLI.
	KeySync bool
	// CuratedModels: the firewall serves an organisation-curated /models.
	CuratedModels bool
}

// Capable is implemented by adapters with optional behaviours.
type Capable interface {
	Capabilities() Capabilities
}

// CapabilitiesOf returns an adapter's capabilities.
func CapabilitiesOf(a Adapter) Capabilities {
	if c, ok := a.(Capable); ok {
		return c.Capabilities()
	}
	return Capabilities{}
}

var registry = []Adapter{
	vulnetixAdapter{},
	fastlyAdapter{},
	kongAdapter{},
	aisgAdapter{},
	customAdapter{},
	openRouterNative{},
	cloudflareNative{},
}

// Adapters returns every adapter in display order.
func Adapters() []Adapter { return slices.Clone(registry) }

// Lookup returns an adapter by id.
func Lookup(id string) (Adapter, bool) {
	for _, a := range registry {
		if a.ID() == id {
			return a, true
		}
	}
	return nil, false
}

// NativeFor returns the provider-native adapter for a provider, if any.
func NativeFor(providerName string) (Adapter, bool) {
	for _, a := range registry {
		if a.Stability() == Native && a.ID() == providerName {
			return a, true
		}
	}
	return nil, false
}

// WithDefaults fills an instance's empty fields from its adapter.
func WithDefaults(inst config.FirewallInstance) config.FirewallInstance {
	a, ok := Lookup(inst.Adapter)
	if !ok {
		return inst
	}
	d := a.Defaults()
	if inst.URL == "" {
		inst.URL = d.URL
	}
	if inst.Mode == "" {
		inst.Mode = d.Mode
		if inst.Header == "" {
			inst.Header = d.Header
		}
	}
	return inst
}

// NeedsSecret reports whether an instance needs its own stored secret.
func NeedsSecret(inst config.FirewallInstance) bool {
	a, ok := Lookup(inst.Adapter)
	if !ok || CapabilitiesOf(a).VulnetixCredential {
		return false
	}
	return WithDefaults(inst).Mode != config.FirewallModeTransparent
}

// Plan checks an instance can carry a provider's traffic and returns its
// route.
func Plan(name string, inst config.FirewallInstance, t Target, secret, account string) (Route, error) {
	inst = WithDefaults(inst)
	a, ok := Lookup(inst.Adapter)
	if !ok {
		return Route{}, fmt.Errorf("unknown firewall adapter %q", inst.Adapter)
	}
	if len(inst.Providers) > 0 && !slices.Contains(inst.Providers, t.Provider) {
		return Route{}, fmt.Errorf("%s is limited to %s", name, strings.Join(inst.Providers, ", "))
	}
	if ok, why := a.Supports(t); !ok {
		return Route{}, fmt.Errorf("%s", why)
	}
	if NeedsSecret(inst) && secret == "" {
		return Route{}, fmt.Errorf("%s has no key: add one in /firewall", name)
	}
	r, err := a.Route(RouteInput{Name: name, Instance: inst, Target: t, Secret: secret, Account: account})
	if err != nil {
		return Route{}, err
	}
	r.Instance, r.AdapterID, r.Label = name, a.ID(), a.Label()
	r.UpstreamBaseURL = t.BaseURL
	return r, nil
}

// supportsCommon is the rule every routing adapter shares: providers whose
// credentials are minted by a sign-in (Copilot, Kiro) or whose URL is itself
// a gateway (Cloudflare) are never routed, and only the three relayed wire
// surfaces are.
func supportsCommon(t Target) (bool, string) {
	switch t.Auth {
	case provider.AuthCopilot, provider.AuthKiro, provider.AuthCFAIG:
		return false, fmt.Sprintf("%s signs in with its own token and is never routed through a firewall", t.Provider)
	}
	switch t.Surface {
	case wire.SurfaceOpenAIChat, wire.SurfaceOpenAIResponses, wire.SurfaceAnthropicMessages:
		return true, ""
	}
	return false, fmt.Sprintf("%s speaks a wire (%s) a firewall does not relay", t.Provider, t.Surface)
}

// modeRoute builds the route for the transparent, authorization and header
// modes.
func modeRoute(in RouteInput) (Route, error) {
	base := strings.TrimRight(strings.ReplaceAll(in.Instance.URL, "{provider}", in.Target.Provider), "/")
	if err := config.ValidFirewallURL(base); err != nil {
		return Route{}, err
	}
	r := Route{Mode: in.Instance.Mode, BaseURL: base}
	switch in.Instance.Mode {
	case config.FirewallModeTransparent:
		r.KeepProviderKey = true
	case config.FirewallModeAuthorization:
		r.APIKey, r.DropProviderAuth = in.Secret, true
		r.KeyHeader, r.KeyValue = "Authorization", "Bearer "+in.Secret
	case config.FirewallModeHeader:
		if !config.ValidFirewallHeader(in.Instance.Header) {
			return Route{}, fmt.Errorf("header %q cannot carry a firewall key", in.Instance.Header)
		}
		r.APIKey, r.DropProviderAuth = in.Secret, true
		r.KeyHeader, r.KeyValue = in.Instance.Header, in.Secret
	default:
		return Route{}, fmt.Errorf("unknown firewall mode %q", in.Instance.Mode)
	}
	return r, nil
}

var allModes = []string{config.FirewallModeTransparent, config.FirewallModeAuthorization, config.FirewallModeHeader}
