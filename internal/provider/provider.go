// Package provider adapts the wire-level shapes into concrete HTTP requests
// for each model provider. Every provider is reached through exactly two
// knobs — base URL and API key — matching the ai-firewall gateway contract.
package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/vulnetix/signet/internal/wire"
)

// Provider reaches a single model provider.
type Provider struct {
	name    string
	baseURL string
	apiKey  string
}

// New validates and returns a Provider. name must be "openai" or "anthropic";
// baseURL must be a valid http(s) URL; apiKey must be non-empty.
func New(name, baseURL, apiKey string) (*Provider, error) {
	n := strings.ToLower(strings.TrimSpace(name))
	if n != "openai" && n != "anthropic" {
		return nil, fmt.Errorf("unsupported provider %q (want openai or anthropic)", name)
	}
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
	return &Provider{name: n, baseURL: baseURL, apiKey: apiKey}, nil
}

// Name returns the normalized provider name.
func (p *Provider) Name() string { return p.name }

// BaseURL returns the configured base URL.
func (p *Provider) BaseURL() string { return p.baseURL }

// IsAnthropic reports whether this provider speaks the Anthropic surface.
func (p *Provider) IsAnthropic() bool { return p.name == "anthropic" }

// IsOpenAI reports whether this provider speaks the OpenAI surfaces.
func (p *Provider) IsOpenAI() bool { return p.name == "openai" }

// Headers returns the auth headers for the provider's surface.
func (p *Provider) Headers() map[string]string {
	return wire.AuthHeaders(p.surface(), p.apiKey)
}

func (p *Provider) surface() wire.Surface {
	if p.IsAnthropic() {
		return wire.SurfaceAnthropicMessages
	}
	return wire.SurfaceOpenAIChat
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
