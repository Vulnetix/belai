package config

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// FirewallSettings configures the AI Firewall: an optional gateway that sits
// between Belai and the model provider. Exactly one configured instance is
// active at a time. Instances and the active choice are the user's own: the
// repo-visible project layer is dropped, and may only turn Enabled off.
type FirewallSettings struct {
	// Enabled routes LLM traffic through the active firewall. Default false.
	Enabled *bool `json:"enabled,omitempty"`
	// Active names the instance in use. Empty means "vulnetix".
	Active string `json:"active,omitempty"`
	// Instances are the configured firewalls, keyed by instance name. The
	// "vulnetix" instance always exists implicitly.
	Instances map[string]FirewallInstance `json:"instances,omitempty"`
}

// FirewallInstance is one configured firewall. Its secret (a gateway key or a
// custom header value) is never stored here: it lives in the credentials
// resolver under FirewallCredentialName(name).
type FirewallInstance struct {
	// Adapter is one of FirewallAdapterIDs.
	Adapter string `json:"adapter"`
	// URL is the firewall's base URL. It may carry a {provider} placeholder,
	// replaced with the Belai provider name. Empty uses the adapter default.
	URL string `json:"url,omitempty"`
	// Mode is transparent, authorization or header. Empty uses the adapter
	// default.
	Mode string `json:"mode,omitempty"`
	// Header names the header that carries the key in header mode.
	Header string `json:"header,omitempty"`
	// Providers limits the firewall to these providers. Empty means every
	// provider the adapter supports.
	Providers []string `json:"providers,omitempty"`
}

// Firewall modes.
const (
	// FirewallModeTransparent only swaps the base URL: the provider's own
	// credential and auth header are sent as normal.
	FirewallModeTransparent = "transparent"
	// FirewallModeAuthorization sends the firewall key as
	// "Authorization: Bearer", and never the provider key (BYOK: the
	// firewall holds the provider key).
	FirewallModeAuthorization = "authorization"
	// FirewallModeHeader sends the firewall key in a named header, and never
	// the provider key (BYOK).
	FirewallModeHeader = "header"
)

// DefaultFirewall is the instance used when none is named.
const DefaultFirewall = "vulnetix"

// DefaultVulnetixGateway is the production Vulnetix AI Firewall host.
const DefaultVulnetixGateway = "https://guardrails.vulnetix.com"

// FirewallAdapterIDs are the adapters an instance may name. The
// provider-native entries (OpenRouter, Cloudflare AI Gateway) are read-only
// and cannot be configured as instances. internal/firewall pins this list
// against its registry.
var FirewallAdapterIDs = []string{"vulnetix", "fastly", "kong", "aisg", "custom"}

// FirewallCredentialName is the credentials-resolver name that holds an
// instance's secret, under the field "api_key". The colon keeps it out of the
// provider namespace, whose names are identifiers.
func FirewallCredentialName(instance string) string {
	return "firewall:" + instance
}

// FirewallEnabled reports whether the AI Firewall is turned on. The new
// firewall.enabled key wins over the legacy vulnetix.firewall_enabled.
func (s Settings) FirewallEnabled() bool {
	if s.Firewall != nil && s.Firewall.Enabled != nil {
		return *s.Firewall.Enabled
	}
	return s.Vulnetix != nil && s.Vulnetix.FirewallEnabled != nil && *s.Vulnetix.FirewallEnabled
}

// FirewallActive returns the active instance name.
func (s Settings) FirewallActive() string {
	if s.Firewall != nil && s.Firewall.Active != "" {
		return s.Firewall.Active
	}
	return DefaultFirewall
}

// FirewallInstanceNamed returns a configured instance. The vulnetix instance
// always exists: its URL comes from its own entry, then the legacy
// vulnetix.gateway_url, then the default.
func (s Settings) FirewallInstanceNamed(name string) (FirewallInstance, bool) {
	var inst FirewallInstance
	ok := false
	if s.Firewall != nil {
		inst, ok = s.Firewall.Instances[name]
	}
	if name == DefaultFirewall {
		inst.Adapter = "vulnetix"
		if inst.URL == "" && s.Vulnetix != nil {
			inst.URL = s.Vulnetix.GatewayURL
		}
		if inst.URL == "" {
			inst.URL = DefaultVulnetixGateway
		}
		return inst, true
	}
	return inst, ok
}

// FirewallInstanceNames lists every instance, vulnetix first, then the rest
// sorted.
func (s Settings) FirewallInstanceNames() []string {
	out := []string{DefaultFirewall}
	if s.Firewall == nil {
		return out
	}
	var rest []string
	for name := range s.Firewall.Instances {
		if name != DefaultFirewall {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// normalizeFirewall folds the legacy vulnetix.firewall_enabled key into
// firewall.enabled on one layer, so every later rule reads a single key. The
// new key wins when both are set.
func normalizeFirewall(s Settings) Settings {
	if s.Vulnetix == nil || s.Vulnetix.FirewallEnabled == nil {
		return s
	}
	fw := FirewallSettings{}
	if s.Firewall != nil {
		fw = *s.Firewall
	}
	if fw.Enabled == nil {
		fw.Enabled = s.Vulnetix.FirewallEnabled
	}
	s.Firewall = &fw
	v := *s.Vulnetix
	v.FirewallEnabled = nil
	s.Vulnetix = &v
	return s
}

// mergeFirewall merges one layer's firewall settings over dst. A project
// layer may only turn the firewall off: it must not route prompts to a
// gateway, name one, or pick one.
func mergeFirewall(dst *FirewallSettings, src *FirewallSettings, project bool) (droppedProject bool) {
	if src.Enabled != nil && (!*src.Enabled || !project) {
		dst.Enabled = src.Enabled
	}
	if project {
		return src.Active != "" || len(src.Instances) > 0
	}
	if src.Active != "" {
		dst.Active = src.Active
	}
	if len(src.Instances) > 0 {
		if dst.Instances == nil {
			dst.Instances = map[string]FirewallInstance{}
		}
		for name, inst := range src.Instances {
			dst.Instances[name] = inst
		}
	}
	return false
}

var (
	firewallNameRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	headerTokenRe    = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]{1,64}$")
	firewallProvider = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
)

// forbiddenFirewallHeaders cannot carry a firewall key: they are hop-by-hop,
// framing, or already set by the harness.
var forbiddenFirewallHeaders = map[string]bool{
	"host": true, "connection": true, "keep-alive": true, "proxy-authorization": true,
	"proxy-connection": true, "te": true, "trailer": true, "transfer-encoding": true,
	"upgrade": true, "content-type": true, "content-length": true, "content-encoding": true,
	"accept": true, "accept-encoding": true, "user-agent": true, "cookie": true, "traceparent": true,
}

// ValidFirewallHeader reports whether name may carry a firewall key.
func ValidFirewallHeader(name string) bool {
	l := strings.ToLower(name)
	return headerTokenRe.MatchString(name) && !forbiddenFirewallHeaders[l] && !strings.HasPrefix(l, "x-belai-")
}

// ValidFirewallURL checks a firewall URL: https, or http to a loopback host,
// with no credentials, query or fragment. A {provider} placeholder is allowed
// in the path.
func ValidFirewallURL(raw string) error {
	u, err := url.Parse(strings.ReplaceAll(raw, "{provider}", "provider"))
	if err != nil || u.Host == "" {
		return fmt.Errorf("firewall url %q is not an absolute URL", raw)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("firewall url %q must not carry credentials, a query or a fragment", raw)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("firewall url %q must use https (plain http is allowed only to a loopback host)", raw)
	}
	return fmt.Errorf("firewall url %q must use https", raw)
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ValidateFirewall checks the firewall settings.
func ValidateFirewall(s Settings) error {
	if s.Firewall == nil {
		return nil
	}
	for name, inst := range s.Firewall.Instances {
		if err := ValidateFirewallInstance(name, inst); err != nil {
			return err
		}
	}
	if a := s.Firewall.Active; a != "" {
		if _, ok := s.FirewallInstanceNamed(a); !ok {
			return fmt.Errorf("firewall.active names %q, which is not a configured firewall", a)
		}
	}
	return nil
}

// ValidateFirewallInstance checks one instance.
func ValidateFirewallInstance(name string, inst FirewallInstance) error {
	if !firewallNameRe.MatchString(name) {
		return fmt.Errorf("firewall name %q must be lowercase letters, digits and dashes", name)
	}
	known := false
	for _, id := range FirewallAdapterIDs {
		if inst.Adapter == id {
			known = true
		}
	}
	if !known {
		return fmt.Errorf("firewall %q: unknown adapter %q (want one of %s)", name, inst.Adapter, strings.Join(FirewallAdapterIDs, ", "))
	}
	if name != DefaultFirewall && inst.Adapter == "vulnetix" {
		return fmt.Errorf("firewall %q: the vulnetix adapter is configured only as the %q instance", name, DefaultFirewall)
	}
	if name == DefaultFirewall && inst.Adapter != "vulnetix" {
		return fmt.Errorf("firewall name %q is reserved for the Vulnetix AI Firewall", name)
	}
	if inst.URL != "" {
		if err := ValidFirewallURL(inst.URL); err != nil {
			return fmt.Errorf("firewall %q: %w", name, err)
		}
	} else if inst.Adapter != "vulnetix" && inst.Adapter != "aisg" {
		return fmt.Errorf("firewall %q: url is required", name)
	}
	if inst.Adapter == "vulnetix" && (inst.Mode != "" || inst.Header != "") {
		return fmt.Errorf("firewall %q: the vulnetix adapter takes no mode or header", name)
	}
	switch inst.Mode {
	case "", FirewallModeTransparent, FirewallModeAuthorization:
		if inst.Header != "" {
			return fmt.Errorf("firewall %q: header is only used in header mode", name)
		}
	case FirewallModeHeader:
		if !ValidFirewallHeader(inst.Header) {
			return fmt.Errorf("firewall %q: header %q cannot carry a firewall key", name, inst.Header)
		}
	default:
		return fmt.Errorf("firewall %q: unknown mode %q (want transparent, authorization or header)", name, inst.Mode)
	}
	for _, p := range inst.Providers {
		if !firewallProvider.MatchString(p) {
			return fmt.Errorf("firewall %q: invalid provider name %q", name, p)
		}
	}
	return nil
}
