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
	"strings"

	"github.com/vulnetix/signet/internal/version"
	"github.com/vulnetix/signet/internal/wire"
)

// Auth names one wire authentication style. It is independent of the wire
// surface a provider speaks: a custom provider may, for example, speak the
// Anthropic surface with a bearer token.
//
// AuthXAPIKey additionally carries an anthropic-version header. That header is
// conceptually a surface concern, but bundling it here preserves today's bytes
// exactly and is correct in practice; do not thread the surface into Provider
// to split it.
type Auth string

const (
	AuthBearer  Auth = "bearer"    // authorization: Bearer <key>
	AuthXAPIKey Auth = "x-api-key" // x-api-key + anthropic-version: 2023-06-01
	AuthCFAIG   Auth = "cf-aig"    // cf-aig-authorization: Bearer <key>
	AuthCopilot Auth = "copilot"   // Bearer + Editor-Version + Copilot-Integration-Id
)

// Valid reports whether a is a known auth style.
func (a Auth) Valid() bool {
	switch a {
	case AuthBearer, AuthXAPIKey, AuthCFAIG, AuthCopilot:
		return true
	}
	return false
}

// Profile describes a provider that is not compiled in: where it lives, which
// wire surface it speaks, how it authenticates, and which models it offers.
type Profile struct {
	BaseURL string
	API     wire.Surface
	Auth    Auth
	Models  []string
}

// builtins is the single source of truth for the compiled-in providers: their
// canonical name and their auth style. New's whitelist, Builtin, and Names all
// derive from it so they cannot drift.
var builtins = []struct {
	name string
	auth Auth
}{
	{"openai", AuthBearer},
	{"anthropic", AuthXAPIKey},
	{"cloudflare-workers-ai", AuthBearer},
	// Cloudflare AI Gateway defaults to gateway-token auth
	// (cf-aig-authorization). When an upstream_api_key is supplied, inference
	// falls back to standard Bearer auth for pass-through gateways.
	{"cloudflare-ai-gateway", AuthCFAIG},
	{"openrouter", AuthBearer},
	{"google-gemini", AuthBearer},
	{"ollama", AuthBearer},
	{"llama", AuthBearer},
	{"github-copilot", AuthCopilot},
	{"huggingface", AuthBearer},
}

// Names returns the supported provider names in a stable order.
func Names() []string {
	out := make([]string, len(builtins))
	for i, b := range builtins {
		out[i] = b.name
	}
	return out
}

// Builtin reports whether name is one of the compiled-in provider names.
func Builtin(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, b := range builtins {
		if b.name == n {
			return true
		}
	}
	return false
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

// New validates and returns a Provider for a built-in name. baseURL must be a
// valid http(s) URL; apiKey must be non-empty.
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
	n := strings.ToLower(strings.TrimSpace(name))
	var auth Auth
	found := false
	for _, b := range builtins {
		if b.name == n {
			auth = b.auth
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("unsupported provider %q (want %s)", name, strings.Join(Names(), ", "))
	}
	return newProvider(n, baseURL, apiKey, auth)
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
	if !validSurface(prof.API) {
		return nil, fmt.Errorf("unknown api surface %q", prof.API)
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
