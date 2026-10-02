package libitem

import (
	"encoding/json"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/vulnetix/belai/internal/config"
)

// Provider limits, shared with the website and the server.
const (
	MaxProviders        = 32
	MaxProviderKey      = 64
	MaxProviderURL      = 1024
	MaxProviderModels   = 256
	MaxProviderText     = 128
	MaxProviderTokens   = 10_000_000
	MaxProviderHost     = 253
	MaxProviderPath     = 128
	MaxProviderInstance = 16
	MaxProviderFWNames  = 64
	MaxProviderEnvName  = 128
)

var (
	providerFields = []string{"name", "providers", "firewall"}
	profileFields  = []string{"base_url", "api", "auth", "api_key_env", "models", "kind", "protocol", "host", "port", "decision_path"}
	modelFields    = []string{"id", "name", "context_window", "max_tokens", "images"}
	firewallFields = []string{"enabled", "active", "instances"}
	instanceFields = []string{"adapter", "url", "mode", "header", "providers"}

	// builtinProviders are Belai's compiled-in provider names and the reserved
	// typesafe: a custom provider may not take one. This is the library's list (the
	// server's); Belai's own validator runs after it and refuses any built-in added
	// since.
	builtinProviders = map[string]bool{
		"openai": true, "anthropic": true, "cloudflare-workers-ai": true, "cloudflare-ai-gateway": true, "openrouter": true,
		"google-gemini": true, "ollama": true, "llama-server": true, "groq": true, "deepseek": true, "fireworks": true,
		"mistral": true, "together": true, "xai": true, "moonshot": true, "minimax": true, "alibaba": true,
		"github-copilot": true, "kiro": true, "huggingface": true, "openai-compatible": true, "typesafe": true,
	}
	surfaces         = map[string]bool{"openai-chat": true, "openai-responses": true, "anthropic-messages": true}
	providerAuths    = map[string]bool{"bearer": true, "x-api-key": true, "cf-aig": true}
	providerKinds    = map[string]bool{"": true, "ollama": true, "llama-server": true, "openai-compatible": true, "jev": true}
	firewallAdapters = []string{"vulnetix", "fastly", "kong", "aisg", "custom"}

	customProviderRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	providerEnvRE    = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	providerHostRE   = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.:-]*[A-Za-z0-9])?$`)
	firewallNameRE   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	firewallProvRE   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	headerTokenRE    = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]{1,64}$")
	portDigitsRE     = regexp.MustCompile(`^[0-9]{1,5}$`)

	// forbiddenFirewallHeaders cannot carry a firewall key: they are hop by hop,
	// framing, or already set by the harness.
	forbiddenFirewallHeaders = map[string]bool{
		"host": true, "connection": true, "keep-alive": true, "proxy-authorization": true,
		"proxy-connection": true, "te": true, "trailer": true, "transfer-encoding": true,
		"upgrade": true, "content-type": true, "content-length": true, "content-encoding": true,
		"accept": true, "accept-encoding": true, "user-agent": true, "cookie": true, "traceparent": true,
	}
)

func init() {
	register(Provider, validateProvider, func(c []byte) error {
		d, err := ParseProvider(c)
		if err != nil {
			return err
		}
		s := config.Settings{Providers: d.Providers, Firewall: d.Firewall}
		if err := config.ValidateProviders(s); err != nil {
			return refuse("%s", clip(err.Error(), 200))
		}
		if err := config.ValidateFirewall(s); err != nil {
			return refuse("%s", clip(err.Error(), 200))
		}
		return nil
	})
}

// ProviderDoc is a validated provider set: the custom providers and the AI
// Firewall instances a host uses, as `providers` and `firewall` in settings. It
// never holds an API key: api_key_env names an environment variable, and a
// firewall instance names the header that carries its key.
type ProviderDoc struct {
	Name      string                            `json:"name"`
	Providers map[string]config.ProviderProfile `json:"providers"`
	Firewall  *config.FirewallSettings          `json:"firewall,omitempty"`
}

// ParseProvider validates a canonical provider document and decodes it.
func ParseProvider(canonical []byte) (ProviderDoc, error) {
	if _, err := validateProvider(canonical); err != nil {
		return ProviderDoc{}, err
	}
	var d ProviderDoc
	if err := json.Unmarshal(canonical, &d); err != nil {
		return ProviderDoc{}, refuse("the document does not match the schema")
	}
	if d.Providers == nil {
		d.Providers = map[string]config.ProviderProfile{}
	}
	return d, nil
}

// ProviderDocument is the library document for the providers and firewall in s,
// under name. The settings types write only what is set, so a document installed
// from the library exports as the same bytes.
func ProviderDocument(name string, s config.Settings) (map[string]any, error) {
	providers := s.Providers
	if providers == nil {
		providers = map[string]config.ProviderProfile{}
	}
	d := map[string]any{"name": name, "providers": providers}
	if fw := s.Firewall; fw != nil && (fw.Enabled != nil || fw.Active != "" || len(fw.Instances) > 0) {
		d["firewall"] = fw
	}
	return d, nil
}

// validateProvider checks a canonical provider document.
func validateProvider(canonical []byte) (string, error) {
	m, err := object(canonical)
	if err != nil {
		return "", err
	}
	if err := onlyKeys(m, "provider", providerFields...); err != nil {
		return "", err
	}
	name, err := docName(m, func(n string) bool { return ValidName(Provider, n) }, nameRule)
	if err != nil {
		return "", err
	}
	providers, present, err := obj(m, "providers", "provider")
	if err != nil {
		return "", err
	}
	if !present {
		return "", refuse("provider.providers is required")
	}
	if len(providers) > MaxProviders {
		return "", refuse("provider.providers has %d entries; the most is %d", len(providers), MaxProviders)
	}
	for _, key := range sortedKeys(providers) {
		p, ok := providers[key].(map[string]any)
		if !ok {
			return "", refuse("provider.providers.%s must be an object", cleanForMessage(key))
		}
		if err := validateProviderProfile(key, p); err != nil {
			return "", err
		}
	}
	fw, hasFirewall, err := obj(m, "firewall", "provider")
	if err != nil {
		return "", err
	}
	if hasFirewall {
		if err := validateFirewallDoc(fw); err != nil {
			return "", err
		}
	}
	return name, nil
}

// validateProviderProfile checks one custom provider, keyed by its name.
func validateProviderProfile(key string, p map[string]any) error {
	if builtinProviders[key] {
		return refuse("provider %q collides with a built-in provider", key)
	}
	if len(key) > MaxProviderKey || !customProviderRE.MatchString(key) {
		return refuse("provider name %q must be lowercase letters, digits, . _ or -, starting with a letter or digit, 64 characters at most", cleanForMessage(key))
	}
	where := "provider.providers." + key
	if err := onlyKeys(p, where, profileFields...); err != nil {
		return err
	}
	baseURL, err := reqStr(p, "base_url", where, MaxProviderURL)
	if err != nil {
		return err
	}
	kind, err := optStr(p, "kind", where, 32, false)
	if err != nil {
		return err
	}
	if !providerKinds[kind] {
		return refuse(`%s.kind %q is unknown (want "ollama", "llama-server", "openai-compatible", "jev" or empty)`, where, cleanForMessage(kind))
	}
	if kind == "jev" {
		if err := checkJevURL(baseURL, where+".base_url"); err != nil {
			return err
		}
	} else if err := checkBaseURL(baseURL, where+".base_url"); err != nil {
		return err
	}
	api, err := optStr(p, "api", where, 32, false)
	if err != nil {
		return err
	}
	if api != "" && !surfaces[api] {
		return refuse("%s.api %q is unknown (want openai-chat, openai-responses or anthropic-messages)", where, cleanForMessage(api))
	}
	if api == "" && kind != "jev" {
		return refuse("%s.api is required (openai-chat, openai-responses or anthropic-messages)", where)
	}
	auth, err := optStr(p, "auth", where, 32, false)
	if err != nil {
		return err
	}
	if auth != "" && !providerAuths[auth] {
		return refuse("%s.auth %q is unknown (want bearer, x-api-key or cf-aig)", where, cleanForMessage(auth))
	}
	env, err := optStr(p, "api_key_env", where, MaxProviderEnvName, false)
	if err != nil {
		return err
	}
	if env != "" && !providerEnvRE.MatchString(env) {
		return refuse("%s.api_key_env %q must be the NAME of an environment variable (capital letters, digits and _), never a key", where, cleanForMessage(env))
	}
	protocol, err := optStr(p, "protocol", where, 8, false)
	if err != nil {
		return err
	}
	if protocol != "" && protocol != "http" && protocol != "https" {
		return refuse("%s.protocol must be http or https", where)
	}
	port, err := optStr(p, "port", where, 8, false)
	if err != nil {
		return err
	}
	if port != "" {
		n, convErr := strconv.Atoi(port)
		if !portDigitsRE.MatchString(port) || convErr != nil || n < 1 || n > 65535 {
			return refuse("%s.port %q is not a TCP port", where, cleanForMessage(port))
		}
	}
	host, err := optStr(p, "host", where, MaxProviderHost, false)
	if err != nil {
		return err
	}
	if host != "" && !providerHostRE.MatchString(host) {
		return refuse("%s.host %q is not a host name", where, cleanForMessage(host))
	}
	path, err := optStr(p, "decision_path", where, MaxProviderPath, false)
	if err != nil {
		return err
	}
	if !validDecisionPath(path) {
		return refuse("%s.decision_path %q must be a plain absolute path with no .., query or fragment", where, cleanForMessage(path))
	}
	return validateProviderModels(p, where)
}

// validDecisionPath is Belai's ValidDecisionPath: empty, or an absolute path of
// printable ASCII with no traversal, query, fragment, backslash or space.
func validDecisionPath(p string) bool {
	if p == "" {
		return true
	}
	if !strings.HasPrefix(p, "/") || strings.Contains(p, "..") || strings.ContainsAny(p, "?#\\ ") || len(p) > MaxProviderPath {
		return false
	}
	for _, r := range p {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}

func validateProviderModels(p map[string]any, where string) error {
	models, _, err := list(p, "models", where, MaxProviderModels)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for i, v := range models {
		mw := where + ".models[" + itoa(i) + "]"
		o, err := entry(v, where+".models", i)
		if err != nil {
			return err
		}
		if err := onlyKeys(o, mw, modelFields...); err != nil {
			return err
		}
		id, err := reqStr(o, "id", mw, MaxProviderText)
		if err != nil {
			return err
		}
		if seen[id] {
			return refuse("%s: the model %q is listed twice", mw, cleanForMessage(id))
		}
		seen[id] = true
		if _, err := optStr(o, "name", mw, MaxProviderText, false); err != nil {
			return err
		}
		for _, f := range []string{"context_window", "max_tokens"} {
			if _, _, err := whole(o, f, mw, 0, MaxProviderTokens, 0); err != nil {
				return err
			}
		}
		if _, err := boolean(o, "images", mw, false); err != nil {
			return err
		}
	}
	return nil
}

// checkBaseURL is Belai's validBaseURL (an http or https URL with a host) plus
// what a library document must also be: no credentials, no space or control
// character, no fragment.
func checkBaseURL(raw, where string) error {
	u, err := parseEndpoint(raw, where)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return refuse("%s must be an http or https URL", where)
	}
	return nil
}

// checkJevURL is Belai's ValidJevURL: https, or http only to a loopback host.
func checkJevURL(raw, where string) error {
	u, err := parseEndpoint(raw, where)
	if err != nil {
		return err
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !loopbackHost(u.Hostname()) {
			return refuse("%s sends tool output in the clear; use https, or http only on localhost", where)
		}
	default:
		return refuse("%s must be https (or http on localhost)", where)
	}
	return nil
}

// parseEndpoint parses a URL that names a service and refuses the shapes no
// endpoint has: whitespace, control characters, a backslash, credentials, a
// fragment, a missing host, an invalid port.
func parseEndpoint(raw, where string) (*url.URL, error) {
	for _, r := range raw {
		if unicode.IsControl(r) || unicode.IsSpace(r) || r == '\\' {
			return nil, refuse("%s holds a space, control character or backslash", where)
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Host == "" || u.Hostname() == "" {
		return nil, refuse("%s is not an absolute URL with a host", where)
	}
	if u.User != nil || strings.Contains(urlAuthority(raw), "@") {
		return nil, refuse("%s must not carry credentials; the key is stored apart", where)
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return nil, refuse("%s must not carry a fragment", where)
	}
	if p := u.Port(); p != "" {
		n, convErr := strconv.Atoi(p)
		if convErr != nil || n < 1 || n > 65535 {
			return nil, refuse("%s has an invalid port", where)
		}
	}
	return u, nil
}

// urlAuthority is the text between :// and the next slash, where a credential
// would sit.
func urlAuthority(raw string) string {
	_, rest, found := strings.Cut(raw, "://")
	if !found {
		rest = raw
	}
	authority, _, _ := strings.Cut(rest, "/")
	return authority
}

// loopbackHost is netguard.IsLoopbackHost: localhost, *.localhost, and the
// loopback addresses.
func loopbackHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if a, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return a.Unmap().IsLoopback()
	}
	return false
}

// validateFirewallDoc checks the firewall block: Belai's ValidateFirewall.
func validateFirewallDoc(fw map[string]any) error {
	const where = "provider.firewall"
	if err := onlyKeys(fw, where, firewallFields...); err != nil {
		return err
	}
	if _, err := boolean(fw, "enabled", where, false); err != nil {
		return err
	}
	active, err := optStr(fw, "active", where, 64, false)
	if err != nil {
		return err
	}
	instances, _, err := obj(fw, "instances", where)
	if err != nil {
		return err
	}
	if len(instances) > MaxProviderInstance {
		return refuse("%s.instances has %d entries; the most is %d", where, len(instances), MaxProviderInstance)
	}
	for _, name := range sortedKeys(instances) {
		inst, ok := instances[name].(map[string]any)
		if !ok {
			return refuse("%s.instances.%s must be an object", where, cleanForMessage(name))
		}
		if err := validateFirewallInstance(name, inst); err != nil {
			return err
		}
	}
	// The vulnetix instance always exists; any other active one must be configured.
	if _, has := instances[active]; active != "" && active != "vulnetix" && !has {
		return refuse("%s.active names %q, which is not a configured firewall", where, cleanForMessage(active))
	}
	return nil
}

func validateFirewallInstance(name string, inst map[string]any) error {
	where := "provider.firewall.instances." + name
	if !firewallNameRE.MatchString(name) {
		return refuse("firewall name %q must be lowercase letters, digits and dashes, 40 characters at most", cleanForMessage(name))
	}
	if err := onlyKeys(inst, where, instanceFields...); err != nil {
		return err
	}
	adapter, err := reqStr(inst, "adapter", where, 16)
	if err != nil {
		return err
	}
	known := false
	for _, id := range firewallAdapters {
		known = known || adapter == id
	}
	if !known {
		return refuse("%s.adapter %q is unknown (want one of %s)", where, cleanForMessage(adapter), strings.Join(firewallAdapters, ", "))
	}
	if name != "vulnetix" && adapter == "vulnetix" {
		return refuse("%s: the vulnetix adapter is configured only as the vulnetix instance", where)
	}
	if name == "vulnetix" && adapter != "vulnetix" {
		return refuse("firewall name vulnetix is reserved for the Vulnetix AI Firewall")
	}
	fwURL, err := optStr(inst, "url", where, MaxProviderURL, false)
	if err != nil {
		return err
	}
	mode, err := optStr(inst, "mode", where, 16, false)
	if err != nil {
		return err
	}
	header, err := optStr(inst, "header", where, 64, false)
	if err != nil {
		return err
	}
	if fwURL != "" {
		if err := checkFirewallURL(fwURL, where+".url"); err != nil {
			return err
		}
	} else if adapter != "vulnetix" && adapter != "aisg" {
		return refuse("%s.url is required for the %s adapter", where, adapter)
	}
	if adapter == "vulnetix" && (mode != "" || header != "") {
		return refuse("%s: the vulnetix adapter takes no mode or header", where)
	}
	switch mode {
	case "", "transparent", "authorization":
		if header != "" {
			return refuse("%s.header is only used in header mode", where)
		}
	case "header":
		if !validFirewallHeader(header) {
			return refuse("%s.header %q cannot carry a firewall key", where, cleanForMessage(header))
		}
	default:
		return refuse("%s.mode %q is unknown (want transparent, authorization or header)", where, cleanForMessage(mode))
	}
	provs, _, err := list(inst, "providers", where, MaxProviderFWNames)
	if err != nil {
		return err
	}
	for i, v := range provs {
		s, ok := v.(string)
		if !ok || !firewallProvRE.MatchString(s) {
			return refuse("%s.providers[%d] is not a provider name", where, i)
		}
	}
	return nil
}

// validFirewallHeader is Belai's ValidFirewallHeader.
func validFirewallHeader(name string) bool {
	l := strings.ToLower(name)
	return headerTokenRE.MatchString(name) && !forbiddenFirewallHeaders[l] && !strings.HasPrefix(l, "x-belai-")
}

// checkFirewallURL is Belai's ValidFirewallURL: https, or http to a loopback host,
// with no credentials, query or fragment; a {provider} placeholder is allowed in
// the path.
func checkFirewallURL(raw, where string) error {
	u, err := parseEndpoint(strings.ReplaceAll(raw, "{provider}", "provider"), where)
	if err != nil {
		return err
	}
	if u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "?") {
		return refuse("%s must not carry a query", where)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !loopbackHost(u.Hostname()) {
			return refuse("%s must use https (plain http is allowed only to a loopback host)", where)
		}
	default:
		return refuse("%s must use https", where)
	}
	return nil
}
