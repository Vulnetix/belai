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

	"github.com/vulnetix/signet/internal/version"
	"github.com/vulnetix/signet/internal/wire"
)

// Provider reaches a single model provider.
type Provider struct {
	name    string
	baseURL string
	apiKey  string
}

// Names returns the supported provider names in a stable order.
func Names() []string {
	return []string{"openai", "anthropic", "cloudflare-workers-ai", "cloudflare-ai-gateway"}
}

// New validates and returns a Provider. Supported names are "openai",
// "anthropic", "cloudflare-workers-ai", and "cloudflare-ai-gateway". baseURL
// must be a valid http(s) URL; apiKey must be non-empty.
func New(name, baseURL, apiKey string) (*Provider, error) {
	n := strings.ToLower(strings.TrimSpace(name))
	switch n {
	case "openai", "anthropic", "cloudflare-workers-ai", "cloudflare-ai-gateway":
	default:
		return nil, fmt.Errorf("unsupported provider %q (want openai, anthropic, cloudflare-workers-ai, or cloudflare-ai-gateway)", name)
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

// IsCloudflareWorkersAI reports whether this provider is Cloudflare Workers AI.
func (p *Provider) IsCloudflareWorkersAI() bool { return p.name == "cloudflare-workers-ai" }

// IsCloudflareAIGateway reports whether this provider is the Cloudflare AI Gateway.
func (p *Provider) IsCloudflareAIGateway() bool { return p.name == "cloudflare-ai-gateway" }

// Headers returns the auth headers for the provider. Cloudflare AI Gateway
// authenticates with cf-aig-authorization; Anthropic with x-api-key; OpenAI
// and Cloudflare Workers AI with a Bearer token — matching Pi's provider auth.
func (p *Provider) Headers() map[string]string {
	h := map[string]string{
		"content-type": "application/json",
		"user-agent":   version.UserAgent(),
	}
	switch p.name {
	case "anthropic":
		h["x-api-key"] = p.apiKey
		h["anthropic-version"] = "2023-06-01"
	case "cloudflare-ai-gateway":
		h["cf-aig-authorization"] = "Bearer " + p.apiKey
	default: // openai, cloudflare-workers-ai
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
