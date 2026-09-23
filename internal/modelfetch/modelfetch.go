// Package modelfetch queries a provider for its live model catalogue. It is
// deliberately leaf-only in the direction that matters: it imports wire,
// provider, and models, never run, so the transport layer does not grow a
// dependency on this fetch layer.
package modelfetch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/vulnetix/signet/internal/aifirewall"
	"github.com/vulnetix/signet/internal/calltrace"
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
	// Kind is the descriptor kind of a custom instance ("ollama",
	// "llama-server", "", or "openai-compatible"). Empty for built-ins and
	// untyped customs, where Name carries the behaviour.
	Kind string
}

// kindOf returns the string that selects a target's behaviour: the explicit
// kind when set, else the provider name. Every dispatch that previously
// switched on t.Name routes through this so a kind'd custom instance inherits
// its template's list parse and context enrichment.
func kindOf(t Target) string {
	if t.Kind != "" {
		return t.Kind
	}
	return t.Name
}

// List fetches the live model catalogue for the target and returns it as
// []models.Model. Static-only targets (openai only) return an empty list with
// no error: OpenAI's live endpoint exposes deprecated and internal identifiers
// that cannot be used as-is.
func List(ctx context.Context, t Target, client *http.Client) ([]models.Model, error) {
	endpoint, err := EndpointFor(t)
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

	if err != nil {
		return nil, err
	}
	req, err := p.NewGetRequest(endpoint)
	if err != nil {
		return nil, err
	}
	req = req.WithContext(ctx)
	calltrace.Apply(ctx, req.Header)
	req.Header.Set("accept", "application/json")

	// Per-target header overrides. The native Gemini model-list API uses a
	// plain API key in x-goog-api-key rather than a Bearer token.
	if t.Name == "google-gemini" {
		req.Header.Del("authorization")
		req.Header.Set("x-goog-api-key", t.APIKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch models: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("provider returned %d", resp.StatusCode)
	}
	return parseModels(ctx, t, client, endpoint, resp)
}

// EndpointFor returns the URL the harness would GET to discover a target's
// live catalogue, or the empty string when the target is static-only (no live
// fetch). It is exported so the TUI can show the exact URL while a fetch is
// in flight.
func EndpointFor(t Target) (string, error) {
	base := strings.TrimRight(t.BaseURL, "/")

	// A few providers need name-specific list-endpoint handling that the
	// generic ListPath cannot capture (base URL transformation or conditional
	// fetching). These exceptions are preserved from the pre-registry code.
	switch t.Name {
	case "cloudflare-ai-gateway":
		// The gateway uses a static catalogue from models.Catalog; there is no
		// gateway-side model list endpoint reachable with only a gateway token.
		return "", nil
	case "google-gemini":
		// The OpenAI-compatible surface omits limits; use the native endpoint.
		base = strings.TrimSuffix(base, "/openai")
		return base + "/models", nil
	case "openai":
		// Do not live-fetch from OpenAI directly: their /v1/models list
		// includes deprecated, preview and internal identifiers that confuse
		// the picker. When the base URL is the Vulnetix gateway, the org's
		// curated catalogue is worth fetching.
		if aifirewall.IsGatewayURL("", base) {
			return base + "/models", nil
		}
		return "", nil
	}

	if d, ok := provider.Lookup(t.Name); ok {
		if d.ListPath == "" {
			return "", nil
		}
		return base + d.ListPath, nil
	}

	// Kind'd custom instance: inherit the template's list endpoint.
	if t.Kind != "" {
		if d, ok := provider.Template(t.Kind); ok && d.ListPath != "" {
			return base + d.ListPath, nil
		}
	}

	// Custom provider: choose by surface.
	switch t.API {
	case wire.SurfaceAnthropicMessages:
		return base + "/v1/models", nil
	default:
		return base + "/models", nil
	}
}

// parseModels decodes a provider-specific response into []models.Model.
func parseModels(ctx context.Context, t Target, client *http.Client, endpoint string, resp *http.Response) ([]models.Model, error) {
	switch kindOf(t) {
	case "anthropic":
		var r struct {
			Data []struct {
				ID             string `json:"id"`
				DisplayName    string `json:"display_name"`
				MaxInputTokens int    `json:"max_input_tokens"`
				MaxTokens      int    `json:"max_tokens"`
				ContextWindow  int    `json:"context_window"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			return nil, err
		}
		out := make([]models.Model, 0, len(r.Data))
		for _, m := range r.Data {
			out = append(out, models.Model{
				ID:            m.ID,
				Label:         firstNonEmpty(m.DisplayName, m.ID),
				Efforts:       models.DefaultEfforts(),
				ContextWindow: firstNonZero(m.ContextWindow, m.MaxInputTokens, m.MaxTokens),
			})
		}
		return out, nil

	case "openrouter":
		var r struct {
			Data []struct {
				ID            string `json:"id"`
				Name          string `json:"name"`
				ContextLength int    `json:"context_length"`
				TopProvider   struct {
					ContextLength int `json:"context_length"`
				} `json:"top_provider"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			return nil, err
		}
		out := make([]models.Model, 0, len(r.Data))
		for _, m := range r.Data {
			cw := m.TopProvider.ContextLength
			if cw == 0 {
				cw = m.ContextLength
			}
			out = append(out, models.Model{
				ID:            m.ID,
				Label:         firstNonEmpty(m.Name, m.ID),
				Efforts:       models.DefaultEfforts(),
				ContextWindow: cw,
			})
		}
		return out, nil

	case "github-copilot":
		var r struct {
			Data []struct {
				ID            string `json:"id"`
				Name          string `json:"name"`
				ContextLength int    `json:"context_length"`
				Capabilities  struct {
					Limits struct {
						MaxContextWindowTokens int `json:"max_context_window_tokens"`
						MaxPromptTokens        int `json:"max_prompt_tokens"`
					} `json:"limits"`
				} `json:"capabilities"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			return nil, err
		}
		out := make([]models.Model, 0, len(r.Data))
		for _, m := range r.Data {
			cw := firstNonZero(
				m.Capabilities.Limits.MaxContextWindowTokens,
				m.Capabilities.Limits.MaxPromptTokens,
				m.ContextLength,
			)
			out = append(out, models.Model{
				ID:            m.ID,
				Label:         firstNonEmpty(m.Name, m.ID),
				Efforts:       models.DefaultEfforts(),
				ContextWindow: cw,
			})
		}
		return out, nil

	case "huggingface":
		var r struct {
			Data []struct {
				ID        string `json:"id"`
				Providers []struct {
					ContextLength int `json:"context_length"`
				} `json:"providers"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			return nil, err
		}
		out := make([]models.Model, 0, len(r.Data))
		for _, m := range r.Data {
			cw := 0
			for _, p := range m.Providers {
				if p.ContextLength > cw {
					cw = p.ContextLength
				}
			}
			out = append(out, models.Model{
				ID:            m.ID,
				Label:         m.ID,
				Efforts:       models.DefaultEfforts(),
				ContextWindow: cw,
			})
		}
		return out, nil

	case "cloudflare-workers-ai":
		// Property values are heterogeneous: a quoted number for the token
		// limits, a bare number on some models, an array for `languages` and
		// `lora`. Decode them raw so one array-valued property cannot fail
		// the whole catalogue.
		var r struct {
			Result []struct {
				Name       string `json:"name"`
				Properties []struct {
					PropertyID string          `json:"property_id"`
					Value      json.RawMessage `json:"value"`
				} `json:"properties"`
			} `json:"result"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			return nil, err
		}
		out := make([]models.Model, 0, len(r.Result))
		for _, m := range r.Result {
			cw := 0
			for _, prop := range m.Properties {
				switch prop.PropertyID {
				case "context_window", "max_total_tokens":
					if v, ok := jsonTokenCount(prop.Value); ok && v > cw {
						cw = v
					}
				}
			}
			out = append(out, models.Model{
				ID:            m.Name,
				Label:         m.Name,
				Efforts:       models.DefaultEfforts(),
				ContextWindow: cw,
			})
		}
		return out, nil

	case "google-gemini":
		var r struct {
			Models []struct {
				Name             string   `json:"name"`
				DisplayName      string   `json:"displayName"`
				InputTokenLimit  int      `json:"inputTokenLimit"`
				SupportedMethods []string `json:"supportedGenerationMethods"`
			} `json:"models"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			return nil, err
		}
		out := make([]models.Model, 0, len(r.Models))
		for _, m := range r.Models {
			if !contains(m.SupportedMethods, "generateContent") {
				continue
			}
			id := strings.TrimPrefix(m.Name, "models/")
			out = append(out, models.Model{
				ID:            id,
				Label:         firstNonEmpty(m.DisplayName, id),
				Efforts:       models.DefaultEfforts(),
				ContextWindow: m.InputTokenLimit,
			})
		}
		return out, nil

	case "ollama":
		// The OpenAI-compatible list carries no context window. Enrich with
		// Ollama's native /api/show endpoint per model. context_length reflects
		// the model's trained window, not the served num_ctx.
		var r struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			return nil, err
		}
		base := strings.TrimRight(t.BaseURL, "/")
		out := make([]models.Model, 0, len(r.Data))
		for _, m := range r.Data {
			out = append(out, models.Model{
				ID:      m.ID,
				Label:   m.ID,
				Efforts: models.DefaultEfforts(),
			})
		}
		if err := enrichOllama(ctx, t, client, base, out); err != nil {
			// Enrichment failure should not fail the list; the windows just
			// stay unknown.
			return out, nil
		}
		return out, nil

	case "llama-server":
		// The OpenAI-compatible list carries no window. The runtime window
		// lives at /props (base minus trailing /v1), which is more
		// authoritative than any static catalogue.
		var r struct {
			Data []struct {
				ID   string `json:"id"`
				Meta struct {
					NCtxTrain int `json:"n_ctx_train"`
				} `json:"meta"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			return nil, err
		}
		rain := 0
		if len(r.Data) > 0 {
			rain = r.Data[0].Meta.NCtxTrain
		}
		cw := rain
		base := strings.TrimRight(t.BaseURL, "/")
		propsURL := strings.TrimSuffix(base, "/v1") + "/props"
		if n, err := llamaPropsNCtx(ctx, t, client, propsURL); err == nil && n > 0 {
			cw = n
		}
		out := make([]models.Model, 0, len(r.Data))
		for _, m := range r.Data {
			out = append(out, models.Model{
				ID:            m.ID,
				Label:         m.ID,
				Efforts:       models.DefaultEfforts(),
				ContextWindow: cw,
			})
		}
		return out, nil

	default: // openai, custom openai-chat
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
			out = append(out, models.Model{
				ID:      m.ID,
				Label:   m.ID,
				Efforts: models.DefaultEfforts(),
			})
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

func firstNonZero(values ...int) int {
	for _, v := range values {
		if v != 0 {
			return v
		}
	}
	return 0
}

// jsonTokenCount reads a token count out of a raw JSON value that a provider
// may encode as either a number or a quoted number. Anything else — an array,
// an object, null, a non-numeric string — reports false.
func jsonTokenCount(raw json.RawMessage) (int, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return 0, false
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return 0, false
		}
		trimmed = strings.TrimSpace(s)
	}
	v, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return 0, false
	}
	return int(v), true
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// enrichOllama fills ContextWindow for each model by calling /api/show with a
// capped concurrency of 4. context_length here is the model's trained window,
// not the served num_ctx.
func enrichOllama(ctx context.Context, t Target, client *http.Client, base string, out []models.Model) error {
	if len(out) == 0 {
		return nil
	}
	p, err := provider.New(t.Name, t.BaseURL, t.APIKey)
	if err != nil && t.API != "" {
		p, err = provider.NewFromProfile(t.Name, provider.Profile{BaseURL: t.BaseURL, API: t.API, Auth: t.Auth}, t.APIKey)
	}
	if err != nil {
		return err
	}

	type result struct {
		idx int
		cw  int
	}

	const maxConcurrency = 4
	sem := make(chan struct{}, maxConcurrency)
	results := make(chan result, len(out))
	var wg sync.WaitGroup

	for i := range out {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			showURL := base + "/api/show"
			body, _ := json.Marshal(map[string]string{"model": out[idx].ID})
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, showURL, bytes.NewReader(body))
			if err != nil {
				return
			}
			for k, v := range p.Headers() {
				req.Header.Set(k, v)
			}
			req.Header.Set("accept", "application/json")

			resp, err := client.Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return
			}
			var info struct {
				ModelInfo map[string]any `json:"model_info"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
				return
			}
			arch, _ := info.ModelInfo["general.architecture"].(string)
			if arch == "" {
				return
			}
			key := arch + ".context_length"
			// Values can arrive as JSON numbers (float64) or strings.
			switch v := info.ModelInfo[key].(type) {
			case float64:
				if v > 0 {
					results <- result{idx: idx, cw: int(v)}
				}
			case string:
				if n, err := strconv.Atoi(v); err == nil && n > 0 {
					results <- result{idx: idx, cw: n}
				}
			case json.Number:
				if n, err := v.Int64(); err == nil && n > 0 {
					results <- result{idx: idx, cw: int(n)}
				}
			}
		}(i)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	for r := range results {
		out[r.idx].ContextWindow = r.cw
	}
	return nil
}

// llamaPropsNCtx returns default_generation_settings.n_ctx from a running
// llama.cpp server's /props endpoint, or an error if the endpoint is missing.
func llamaPropsNCtx(ctx context.Context, t Target, client *http.Client, propsURL string) (int, error) {
	p, err := provider.New(t.Name, t.BaseURL, t.APIKey)
	if err != nil && t.API != "" {
		p, err = provider.NewFromProfile(t.Name, provider.Profile{BaseURL: t.BaseURL, API: t.API, Auth: t.Auth}, t.APIKey)
	}
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, propsURL, nil)
	if err != nil {
		return 0, err
	}
	for k, v := range p.Headers() {
		req.Header.Set(k, v)
	}
	req.Header.Set("accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("props returned %d", resp.StatusCode)
	}
	var info struct {
		DefaultGenerationSettings struct {
			NCtx int `json:"n_ctx"`
		} `json:"default_generation_settings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return 0, err
	}
	return info.DefaultGenerationSettings.NCtx, nil
}
