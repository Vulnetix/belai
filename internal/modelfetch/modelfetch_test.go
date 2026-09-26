package modelfetch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/vulnetix/belai/internal/provider"
	"github.com/vulnetix/belai/internal/wire"
)

func TestListOpenAINoLiveFetch(t *testing.T) {
	// OpenAI is excluded from live fetching because /v1/models returns
	// deprecated, preview and internal identifiers that confuse the picker
	// and fail at request time.  Only the curated static catalogue is shown.
	models, err := List(context.Background(), Target{Name: "openai", BaseURL: "http://example.com", APIKey: "k"}, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(models) != 0 {
		t.Fatalf("expected empty live-fetch list for openai, got %+v", models)
	}
}

func TestListAnthropic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "claude-x", "display_name": "Claude X"}}})
	}))
	defer srv.Close()

	models, err := List(context.Background(), Target{Name: "anthropic", BaseURL: srv.URL, APIKey: "k"}, srv.Client())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(models) != 1 || models[0].ID != "claude-x" || models[0].Label != "Claude X" {
		t.Fatalf("models = %+v", models)
	}
}

func TestListOpenRouterContextLength(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "openai/gpt-4o", "name": "GPT-4o", "context_length": 128000}}})
	}))
	defer srv.Close()

	models, err := List(context.Background(), Target{Name: "openrouter", BaseURL: srv.URL, APIKey: "k"}, srv.Client())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(models) != 1 || models[0].ContextWindow != 128000 {
		t.Fatalf("models = %+v", models)
	}
}

func TestListCloudflareWorkersAI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"result": []any{map[string]any{"name": "@cf/meta/llama-3"}}})
	}))
	defer srv.Close()

	models, err := List(context.Background(), Target{Name: "cloudflare-workers-ai", BaseURL: srv.URL, APIKey: "k"}, srv.Client())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(models) != 1 || models[0].ID != "@cf/meta/llama-3" {
		t.Fatalf("models = %+v", models)
	}
}

func TestListCloudflareAIGatewayIsStatic(t *testing.T) {
	// The gateway has no reachable model-list endpoint with only a gateway
	// token; the catalogue comes from the static models.Catalog list.
	models, err := List(context.Background(), Target{Name: "cloudflare-ai-gateway", BaseURL: "https://gateway.ai.cloudflare.com/v1/acct/default/compat", APIKey: "k"}, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(models) != 0 {
		t.Fatalf("expected empty live-fetch list for cloudflare-ai-gateway, got %+v", models)
	}
}

func TestListHuggingFace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer k" {
			t.Fatalf("Authorization = %q, want Bearer k", auth)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "meta-llama/Llama-3.1-8B-Instruct"}}})
	}))
	t.Cleanup(srv.Close)
	models, err := List(context.Background(), Target{Name: "huggingface", BaseURL: srv.URL + "/v1", APIKey: "k"}, srv.Client())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(models) != 1 || models[0].ID != "meta-llama/Llama-3.1-8B-Instruct" {
		t.Fatalf("models = %+v", models)
	}
}

func TestListCustomSurface(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "m1"}}})
	}))
	defer srv.Close()

	models, err := List(context.Background(), Target{Name: "my-llm", BaseURL: srv.URL, APIKey: "k", API: wire.SurfaceAnthropicMessages, Auth: provider.AuthXAPIKey}, srv.Client())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(models) != 1 || models[0].ID != "m1" {
		t.Fatalf("models = %+v", models)
	}
}

// Every fetched model must carry the default effort set so the model picker
// and commitModel never write an empty effort for a live-fetched entry.
func TestEveryFetchedModelHasEfforts(t *testing.T) {
	for _, name := range []string{"anthropic", "openrouter", "github-copilot", "huggingface", "cloudflare-workers-ai", "google-gemini"} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch name {
				case "anthropic":
					_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "m", "max_input_tokens": 100}}})
				case "openrouter":
					_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "m", "top_provider": map[string]any{"context_length": 100}}}})
				case "github-copilot":
					_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "m", "capabilities": map[string]any{"limits": map[string]any{"max_context_window_tokens": 100}}}}})
				case "huggingface":
					_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "m", "providers": []any{map[string]any{"context_length": 100}}}}})
				case "cloudflare-workers-ai":
					_ = json.NewEncoder(w).Encode(map[string]any{"result": []any{map[string]any{"name": "m", "properties": []any{map[string]any{"property_id": "context_window", "value": "100"}}}}})
				case "google-gemini":
					_ = json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]any{"name": "models/m", "inputTokenLimit": 100, "supportedGenerationMethods": []any{"generateContent"}}}})
				}
			}))
			t.Cleanup(srv.Close)

			models, err := List(context.Background(), Target{Name: name, BaseURL: srv.URL, APIKey: "k"}, srv.Client())
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(models) != 1 {
				t.Fatalf("expected 1 model, got %+v", models)
			}
			if len(models[0].Efforts) == 0 {
				t.Fatalf("model %q has no Efforts", models[0].ID)
			}
		})
	}
}

func TestListAnthropicContextWindow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{
			map[string]any{"id": "claude-7", "display_name": "Claude 7", "max_input_tokens": 200000, "max_tokens": 8192},
		}})
	}))
	defer srv.Close()

	models, err := List(context.Background(), Target{Name: "anthropic", BaseURL: srv.URL, APIKey: "k"}, srv.Client())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(models) != 1 || models[0].ContextWindow != 200000 {
		t.Fatalf("models = %+v", models)
	}
}

func TestListAnthropicFallsBackToContextWindowKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{
			map[string]any{"id": "claude-proxy", "context_window": 32768},
		}})
	}))
	defer srv.Close()

	models, err := List(context.Background(), Target{Name: "anthropic", BaseURL: srv.URL, APIKey: "k"}, srv.Client())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(models) != 1 || models[0].ContextWindow != 32768 {
		t.Fatalf("models = %+v", models)
	}
}

func TestListOpenRouterPrefersTopProviderContextLength(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{
			"id":             "openai/gpt-4o",
			"name":           "GPT-4o",
			"context_length": 128000,
			"top_provider":   map[string]any{"context_length": 256000},
		}}})
	}))
	defer srv.Close()

	models, err := List(context.Background(), Target{Name: "openrouter", BaseURL: srv.URL, APIKey: "k"}, srv.Client())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(models) != 1 || models[0].ContextWindow != 256000 {
		t.Fatalf("models = %+v", models)
	}
}

func TestListGitHubCopilotContextWindow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "Bearer k" {
			t.Fatalf("Authorization = %q, want Bearer k", auth)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{
			"id":   "gpt-4o-copilot",
			"name": "GPT-4o Copilot",
			"capabilities": map[string]any{
				"limits": map[string]any{
					"max_context_window_tokens": 128000,
					"max_prompt_tokens":         64000,
				},
			},
		}}})
	}))
	defer srv.Close()

	models, err := List(context.Background(), Target{Name: "github-copilot", BaseURL: srv.URL, APIKey: "k"}, srv.Client())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(models) != 1 || models[0].ContextWindow != 128000 {
		t.Fatalf("models = %+v", models)
	}
}

func TestListHuggingFaceContextWindow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{
			"id":        "meta-llama/Llama-3.1-8B-Instruct",
			"providers": []any{map[string]any{"context_length": 32768}},
		}}})
	}))
	t.Cleanup(srv.Close)
	models, err := List(context.Background(), Target{Name: "huggingface", BaseURL: srv.URL + "/v1", APIKey: "k"}, srv.Client())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(models) != 1 || models[0].ContextWindow != 32768 {
		t.Fatalf("models = %+v", models)
	}
}

func TestListCloudflareWorkersAIContextWindow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ai/models/search" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": []any{map[string]any{
			"name": "@cf/meta/llama-3",
			"properties": []any{
				map[string]any{"property_id": "context_window", "value": "8192"},
				map[string]any{"property_id": "max_total_tokens", "value": "4096"},
			},
		}}})
	}))
	defer srv.Close()

	models, err := List(context.Background(), Target{Name: "cloudflare-workers-ai", BaseURL: srv.URL, APIKey: "k"}, srv.Client())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(models) != 1 || models[0].ContextWindow != 8192 {
		t.Fatalf("models = %+v", models)
	}
}

func TestListGoogleGemini(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		// Native endpoint uses a plain API key, not a Bearer token.
		if auth := r.Header.Get("Authorization"); auth != "" {
			t.Fatalf("Authorization = %q, want absent", auth)
		}
		if key := r.Header.Get("X-Goog-Api-Key"); key != "k" {
			t.Fatalf("X-Goog-Api-Key = %q, want k", key)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"models": []any{
			map[string]any{"name": "models/gemini-2.5-pro", "displayName": "Gemini 2.5 Pro", "inputTokenLimit": 1000000, "supportedGenerationMethods": []any{"generateContent"}},
			map[string]any{"name": "models/text-embedding-004", "inputTokenLimit": 2048, "supportedGenerationMethods": []any{"embedContent"}},
		}})
	}))
	defer srv.Close()

	models, err := List(context.Background(), Target{Name: "google-gemini", BaseURL: srv.URL + "/openai", APIKey: "k"}, srv.Client())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("expected only generateContent models, got %+v", models)
	}
	if models[0].ID != "gemini-2.5-pro" || models[0].Label != "Gemini 2.5 Pro" || models[0].ContextWindow != 1000000 {
		t.Fatalf("models = %+v", models)
	}
}

func TestListOllamaEnrichesContextWindow(t *testing.T) {
	var showCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "llama3.1"}, map[string]any{"id": "phi4"}}})
		case "/api/show":
			showCalls.Add(1)
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			info := map[string]any{
				"model_info": map[string]any{
					"general.architecture": "llama",
					"llama.context_length": float64(131072),
				},
			}
			if body["model"] == "phi4" {
				info = map[string]any{
					"model_info": map[string]any{
						"general.architecture": "phi3",
						"phi3.context_length":  float64(16384),
					},
				}
			}
			_ = json.NewEncoder(w).Encode(info)
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	models, err := List(context.Background(), Target{Name: "ollama", BaseURL: srv.URL, APIKey: "k"}, srv.Client())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %+v", models)
	}
	if showCalls.Load() == 0 {
		t.Fatal("expected /api/show enrichment calls")
	}
	want := map[string]int{"llama3.1": 131072, "phi4": 16384}
	for _, m := range models {
		if m.ContextWindow != want[m.ID] {
			t.Fatalf("%s context window = %d, want %d", m.ID, m.ContextWindow, want[m.ID])
		}
	}
}

func TestListOllamaStillReturnsListWhenEnrichmentFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "llama3.1"}}})
			return
		}
		if r.URL.Path == "/api/show" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		t.Fatalf("unexpected path %q", r.URL.Path)
	}))
	defer srv.Close()

	models, err := List(context.Background(), Target{Name: "ollama", BaseURL: srv.URL, APIKey: "k"}, srv.Client())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(models) != 1 || models[0].ContextWindow != 0 {
		t.Fatalf("models = %+v", models)
	}
}

func TestListLlamaServerEnrichesFromProps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "custom-llama", "meta": map[string]any{"n_ctx_train": 32768}}}})
		case "/props":
			_ = json.NewEncoder(w).Encode(map[string]any{"default_generation_settings": map[string]any{"n_ctx": 16384}})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	models, err := List(context.Background(), Target{Name: "llama-server", BaseURL: srv.URL + "/v1", APIKey: "k"}, srv.Client())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(models) != 1 || models[0].ContextWindow != 16384 {
		t.Fatalf("models = %+v", models)
	}
}

func TestListLlamaServerFallsBackToMeta(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "custom-llama", "meta": map[string]any{"n_ctx_train": 32768}}}})
		case "/props":
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	models, err := List(context.Background(), Target{Name: "llama-server", BaseURL: srv.URL + "/v1", APIKey: "k"}, srv.Client())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(models) != 1 || models[0].ContextWindow != 32768 {
		t.Fatalf("models = %+v", models)
	}
}

// Workers AI property values are not all strings: `languages` is an array and
// numeric limits arrive unquoted. A single non-string value must not fail the
// whole catalogue decode.
func TestListCloudflareWorkersAIMixedPropertyValues(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"result": []any{
			map[string]any{"name": "@cf/meta/llama-3.1-8b-instruct", "properties": []any{
				map[string]any{"property_id": "languages", "value": []any{"en", "de"}},
				map[string]any{"property_id": "lora", "value": []any{map[string]any{"name": "x"}}},
				map[string]any{"property_id": "context_window", "value": "8192"},
			}},
			map[string]any{"name": "@cf/meta/llama-3.3-70b-instruct", "properties": []any{
				map[string]any{"property_id": "max_total_tokens", "value": 24000},
			}},
		}})
	}))
	t.Cleanup(srv.Close)

	got, err := List(context.Background(), Target{Name: "cloudflare-workers-ai", BaseURL: srv.URL, APIKey: "k"}, srv.Client())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 models, got %+v", got)
	}
	if got[0].ContextWindow != 8192 {
		t.Fatalf("string value: want 8192, got %d", got[0].ContextWindow)
	}
	if got[1].ContextWindow != 24000 {
		t.Fatalf("number value: want 24000, got %d", got[1].ContextWindow)
	}
}

// TestListNewOpenAICompatibleProviders verifies that the nine new providers
// introduced in the registry fall through to the default OpenAI /models
// parser, returning model ids and assigning the default effort set.
func TestListNewOpenAICompatibleProviders(t *testing.T) {
	providers := []string{"groq", "deepseek", "fireworks", "mistral", "together", "xai", "moonshot", "minimax", "alibaba"}
	for _, name := range providers {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/models" {
					t.Fatalf("path = %q, want /models", r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": name + "/m1"}}})
			}))
			t.Cleanup(srv.Close)

			models, err := List(context.Background(), Target{Name: name, BaseURL: srv.URL, APIKey: "k"}, srv.Client())
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(models) != 1 || models[0].ID != name+"/m1" {
				t.Fatalf("models = %+v", models)
			}
			if len(models[0].Efforts) == 0 {
				t.Fatalf("model %q has no Efforts", models[0].ID)
			}
		})
	}
}

func TestListNewProviderEndpoint(t *testing.T) {
	endpoint, err := EndpointFor(Target{Name: "groq", BaseURL: "https://api.groq.com/openai/v1"})
	if err != nil {
		t.Fatalf("EndpointFor: %v", err)
	}
	if want := "https://api.groq.com/openai/v1/models"; endpoint != want {
		t.Fatalf("endpoint = %q, want %q", endpoint, want)
	}
}

func TestListKindOllamaCustomHitsModelsAndShow(t *testing.T) {
	var showCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "llama3.1"}}})
		case "/api/show":
			showCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"model_info": map[string]any{
					"general.architecture": "llama",
					"llama.context_length": float64(131072),
				},
			})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	models, err := List(context.Background(), Target{
		Name: "ollama-gpu", Kind: "ollama", BaseURL: srv.URL, APIKey: "k",
		API: wire.SurfaceOpenAIChat, Auth: provider.AuthBearer,
	}, srv.Client())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(models) != 1 || models[0].ContextWindow != 131072 {
		t.Fatalf("models = %+v", models)
	}
	if showCalls.Load() == 0 {
		t.Fatal("expected /api/show enrichment calls for a kind:ollama custom")
	}
}

func TestListKindLlamaServerCustomHitsProps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "custom-llama", "meta": map[string]any{"n_ctx_train": 32768}}}})
		case "/props":
			_ = json.NewEncoder(w).Encode(map[string]any{"default_generation_settings": map[string]any{"n_ctx": 16384}})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	models, err := List(context.Background(), Target{
		Name: "llama-gpu", Kind: "llama-server", BaseURL: srv.URL + "/v1", APIKey: "k",
		API: wire.SurfaceOpenAIChat, Auth: provider.AuthBearer,
	}, srv.Client())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(models) != 1 || models[0].ContextWindow != 16384 {
		t.Fatalf("models = %+v", models)
	}
}
