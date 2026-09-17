// Package modelfetch queries a provider for its live model catalogue. It is
// deliberately leaf-only in the direction that matters: it imports wire,
// provider, and models, never run, so the transport layer does not grow a
// dependency on this fetch layer.
package modelfetch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/vulnetix/signet/internal/httpclient"
	"github.com/vulnetix/signet/internal/models"
	"github.com/vulnetix/signet/internal/provider"
	"github.com/vulnetix/signet/internal/wire"
)

// Target identifies a provider endpoint and its auth.
type Target struct {
	Name    string
	BaseURL string
	APIKey  string
	Auth    provider.Auth
	API     wire.Surface // custom providers only
}

// List fetches the live model catalogue for the target and returns it as
// []models.Model. Static-only targets (cloudflare-ai-gateway, huggingface)
// return an empty list with no error.
func List(ctx context.Context, t Target, client *http.Client) ([]models.Model, error) {
	endpoint, err := endpointFor(t)
	if err != nil {
		return nil, err
	}
	if endpoint == "" {
		return nil, nil // static-only passthrough
	}
	if client == nil {
		client = httpclient.Default()
	}

	p, err := provider.New(t.Name, t.BaseURL, t.APIKey)
	if err != nil && t.API != "" {
		p, err = provider.NewFromProfile(t.Name, provider.Profile{BaseURL: t.BaseURL, API: t.API, Auth: t.Auth}, t.APIKey)
	}
	if t.Name == "cloudflare-ai-gateway" && err == nil {
		// The gateway itself has no discoverable model list, but every
		// gateway can run Workers AI models. Query the account's Workers AI
		// catalog, which requires the standard Cloudflare v4 Bearer auth rather
		// than the gateway's cf-aig-authorization style.
		p, err = provider.New("cloudflare-workers-ai", t.BaseURL, t.APIKey)
	}
	if err != nil {
		return nil, err
	}
	req, err := p.NewGetRequest(endpoint)
	if err != nil {
		return nil, err
	}
	req = req.WithContext(ctx)
	req.Header.Set("accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch models: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("provider returned %d", resp.StatusCode)
	}
	return parseModels(t, resp)
}

func endpointFor(t Target) (string, error) {
	base := strings.TrimRight(t.BaseURL, "/")
	switch t.Name {
	case "cloudflare-ai-gateway":
		// The gateway is a passthrough with no model list endpoint. Reuse the
		// account's Workers AI catalog (Cloudflare v4 API). The gateway base
		// URL embeds the account id: https://gateway.ai.cloudflare.com/v1/{account_id}/{gateway_id}.
		u, err := url.Parse(base)
		if err != nil {
			return "", fmt.Errorf("cloudflare-ai-gateway base URL: %w", err)
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) < 2 || parts[0] != "v1" || parts[1] == "" {
			return "", fmt.Errorf("cloudflare-ai-gateway base URL %q missing account_id", t.BaseURL)
		}
		host := u.Host
		if host == "gateway.ai.cloudflare.com" {
			host = "api.cloudflare.com"
		}
		return fmt.Sprintf("%s://%s/client/v4/accounts/%s/ai/models/search", u.Scheme, host, parts[1]), nil
	case "anthropic":
		return base + "/v1/models", nil
	case "cloudflare-workers-ai":
		return base + "/ai/models/search", nil
	case "huggingface":
		return base + "/models", nil
	case "openrouter", "google-gemini", "ollama", "llama-server", "github-copilot":
		return base + "/models", nil
	case "openai":
		// Do not live-fetch: OpenAI /v1/models includes deprecated, preview and
		// internal identifiers that confuse the picker and fail at request time.
		return "", nil
	default:
		// Custom provider: choose by surface.
		switch t.API {
		case wire.SurfaceAnthropicMessages:
			return base + "/v1/models", nil
		default:
			return base + "/models", nil
		}
	}
}

// parseModels decodes a provider-specific response into []models.Model.
func parseModels(t Target, resp *http.Response) ([]models.Model, error) {
	switch t.Name {
	case "anthropic":
		var r struct {
			Data []struct {
				ID          string `json:"id"`
				DisplayName string `json:"display_name"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			return nil, err
		}
		out := make([]models.Model, 0, len(r.Data))
		for _, m := range r.Data {
			out = append(out, models.Model{ID: m.ID, Label: firstNonEmpty(m.DisplayName, m.ID)})
		}
		return out, nil

	case "openrouter", "github-copilot":
		var r struct {
			Data []struct {
				ID            string `json:"id"`
				Name          string `json:"name"`
				ContextLength int    `json:"context_length"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			return nil, err
		}
		out := make([]models.Model, 0, len(r.Data))
		for _, m := range r.Data {
			out = append(out, models.Model{ID: m.ID, Label: firstNonEmpty(m.Name, m.ID), ContextWindow: m.ContextLength})
		}
		return out, nil

	case "cloudflare-workers-ai", "cloudflare-ai-gateway":
		var r struct {
			Result []struct {
				Name string `json:"name"`
			} `json:"result"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			return nil, err
		}
		out := make([]models.Model, 0, len(r.Result))
		for _, m := range r.Result {
			out = append(out, models.Model{ID: m.Name, Label: m.Name})
		}
		return out, nil

	default: // openai, google-gemini, ollama, llama, custom openai-chat
		var r struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			return nil, err
		}
		out := make([]models.Model, 0, len(r.Data))
		for _, m := range r.Data {
			out = append(out, models.Model{ID: m.ID, Label: m.ID})
		}
		return out, nil
	}
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
