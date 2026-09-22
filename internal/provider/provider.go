// Package provider adapts the wire-level shapes into concrete HTTP requests
// for each model provider. Every provider is reached through a base URL, an
// API key, a wire surface, and an auth style — matching the ai-firewall
// gateway contract.
package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/vulnetix/signet/internal/version"
	"github.com/vulnetix/signet/internal/wire"
)

// Profile describes a provider that is not compiled in: where it lives, which
// wire surface it speaks, how it authenticates, and which models it offers.
type Profile struct {
	BaseURL string
	API     wire.Surface
	Auth    Auth
	Models  []string
	// Kind names the built-in descriptor this instance is templated from
	// ("ollama", "llama-server", "", or "openai-compatible"). An unknown
	// kind is rejected so a profile can never invent new behaviour.
	Kind string
}

// Lookup returns the descriptor for a compiled-in provider, if any.
func Lookup(name string) (Descriptor, bool) {
	d, ok := registry[strings.ToLower(strings.TrimSpace(name))]
	return d, ok
}

// Names returns the supported provider names in a stable order.
func Names() []string {
	out := make([]string, 0, len(registry))
	for name := range registry {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Builtin reports whether name is one of the compiled-in provider names.
func Builtin(name string) bool {
	_, ok := Lookup(name)
	return ok
}

// ValidCustomName reports whether name is a safe custom-provider name. The
// name is used to build keychain accounts (a ":" would collide across
// providers), to key credential JSON, and — critically — it is written into
// the sealed system prompt, so an unconstrained name is prompt-injection
// surface inside a trusted block. Charset: lowercase, alphanumeric first
// character, then [a-z0-9._-].
func ValidCustomName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
			if i == 0 {
				return false
			}
		case c == '.' || c == '_' || c == '-':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// Provider reaches a single model provider.
type Provider struct {
	name    string
	baseURL string
	apiKey  string
	auth    Auth
}

// NewWithAuth is like New but allows the caller to override the compiled-in
// auth style. It is used when a built-in provider can be reached through more
// than one authentication path (e.g., cloudflare-ai-gateway as either a
// gateway-token endpoint or an upstream-key pass-through).
func NewWithAuth(name, baseURL, apiKey string, auth Auth) (*Provider, error) {
	return newProvider(name, baseURL, apiKey, auth)
}

// New validates and returns a Provider for a built-in name. baseURL must be a
// valid http(s) URL; apiKey must be non-empty.
func New(name, baseURL, apiKey string) (*Provider, error) {
	d, ok := Lookup(name)
	if !ok {
		return nil, fmt.Errorf("unsupported provider %q (want %s)", name, strings.Join(Names(), ", "))
	}
	return newProvider(name, baseURL, apiKey, d.Auth)
}

// NewFromProfile validates and returns a Provider from a custom profile. It
// rejects any built-in name so a settings file cannot shadow a compiled-in
// provider with an attacker-controlled base URL, and rejects any name failing
// ValidCustomName.
func NewFromProfile(name string, prof Profile, apiKey string) (*Provider, error) {
	if Builtin(name) {
		return nil, fmt.Errorf("provider name %q is built-in; use New for built-ins", name)
	}
	if !ValidCustomName(name) {
		return nil, fmt.Errorf("invalid custom provider name %q", name)
	}
	if !prof.Auth.Valid() {
		return nil, fmt.Errorf("unknown auth style %q", prof.Auth)
	}
	if _, ok := Template(prof.Kind); !ok {
		return nil, fmt.Errorf("unknown provider kind %q", prof.Kind)
	}
	if !validSurface(prof.API) {
		return nil, fmt.Errorf("unknown api surface %q", prof.API)
	}
	// Copilot auth is deliberately reserved for the built-in provider so a
	// custom profile cannot impersonate GitHub Copilot.
	if prof.Auth == AuthCopilot {
		return nil, fmt.Errorf("auth style %q is reserved for the built-in github-copilot provider", prof.Auth)
	}
	return newProvider(name, prof.BaseURL, apiKey, prof.Auth)
}

func validSurface(s wire.Surface) bool {
	switch s {
	case wire.SurfaceOpenAIChat, wire.SurfaceOpenAIResponses, wire.SurfaceAnthropicMessages:
		return true
	}
	return false
}

// newProvider validates the parts shared by every constructor: base URL and
// key. It exists once so the validation cannot drift between built-in and
// custom paths.
func newProvider(name, baseURL, apiKey string, auth Auth) (*Provider, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return nil, fmt.Errorf("base_url is required")
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("invalid base_url %q", baseURL)
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("api_key is required")
	}
	return &Provider{name: name, baseURL: baseURL, apiKey: apiKey, auth: auth}, nil
}

// Name returns the normalized provider name.
func (p *Provider) Name() string { return p.name }

// BaseURL returns the configured base URL.
func (p *Provider) BaseURL() string { return p.baseURL }

// Auth returns the provider's auth style.
func (p *Provider) Auth() Auth { return p.auth }

// Headers returns the auth headers for the provider. Cloudflare AI Gateway
// authenticates with cf-aig-authorization; Anthropic with x-api-key; OpenAI
// and Cloudflare Workers AI with a Bearer token — matching Pi's provider auth.

func (p *Provider) Headers() map[string]string {
	h := map[string]string{
		"content-type": "application/json",
		"user-agent":   version.UserAgent(),
	}
	switch p.auth {
	case AuthXAPIKey:
		h["x-api-key"] = p.apiKey
		h["anthropic-version"] = "2023-06-01"
	case AuthCFAIG:
		h["cf-aig-authorization"] = "Bearer " + p.apiKey
	case AuthCopilot:
		h["authorization"] = "Bearer " + p.apiKey
		h["editor-version"] = "vscode/1.85.0"
		h["copilot-integration-id"] = "vscode-chat"
	default: // AuthBearer
		h["authorization"] = "Bearer " + p.apiKey
	}
	return h
}

func (p *Provider) chatURL() string {
	return wire.BuildURL(p.baseURL, wire.SurfaceOpenAIChat)
}

func (p *Provider) responsesURL() string {
	return wire.BuildURL(p.baseURL, wire.SurfaceOpenAIResponses)
}

func (p *Provider) messagesURL() string {
	return wire.BuildURL(p.baseURL, wire.SurfaceAnthropicMessages)
}

// newRequest builds a JSON POST request with the provider's auth headers.
func (p *Provider) newRequest(endpoint string, body any) (*http.Request, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request body: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	for k, v := range p.Headers() {
		req.Header.Set(k, v)
	}
	return req, nil
}

// NewGetRequest builds a GET request with the provider's auth headers, for
// read-only endpoints such as /models.
func (p *Provider) NewGetRequest(endpoint string) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build get request: %w", err)
	}
	for k, v := range p.Headers() {
		req.Header.Set(k, v)
	}
	return req, nil
}
