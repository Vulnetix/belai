package modelfetch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vulnetix/signet/internal/provider"
	"github.com/vulnetix/signet/internal/wire"
)

func TestListOpenAI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "gpt-5"}}})
	}))
	defer srv.Close()

	models, err := List(context.Background(), Target{Name: "openai", BaseURL: srv.URL, APIKey: "k"}, srv.Client())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(models) != 1 || models[0].ID != "gpt-5" {
		t.Fatalf("models = %+v", models)
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

func TestListGatewayStaticOnly(t *testing.T) {
	models, err := List(context.Background(), Target{Name: "cloudflare-ai-gateway", BaseURL: "https://x", APIKey: "k"}, nil)
	if err != nil || models != nil {
		t.Fatalf("gateway should return nil models with no error, got %v, %v", models, err)
	}
}

func TestListHuggingFaceStaticOnly(t *testing.T) {
	models, err := List(context.Background(), Target{Name: "huggingface", BaseURL: "https://x", APIKey: "k"}, nil)
	if err != nil || models != nil {
		t.Fatalf("huggingface should return nil models with no error, got %v, %v", models, err)
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
