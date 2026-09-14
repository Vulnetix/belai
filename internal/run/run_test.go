package run

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestResolveCloudflareWorkersAI(t *testing.T) {
	cfg, err := Resolve("", "cloudflare-workers-ai", envMap(map[string]string{
		"CLOUDFLARE_API_KEY":    "k",
		"CLOUDFLARE_ACCOUNT_ID": "acct123",
	}))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.BaseURL != "https://api.cloudflare.com/client/v4/accounts/acct123" {
		t.Fatalf("BaseURL = %q", cfg.BaseURL)
	}
	if cfg.Model != "@cf/moonshotai/kimi-k2.6" {
		t.Fatalf("default Model = %q", cfg.Model)
	}
}

func TestResolveCloudflareAIGateway(t *testing.T) {
	cfg, err := Resolve("claude-sonnet-4-5", "cloudflare-ai-gateway", envMap(map[string]string{
		"CLOUDFLARE_API_KEY":    "k",
		"CLOUDFLARE_ACCOUNT_ID": "acct",
		"CLOUDFLARE_GATEWAY_ID": "gw",
	}))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.BaseURL != "https://gateway.ai.cloudflare.com/v1/acct/gw" {
		t.Fatalf("BaseURL = %q", cfg.BaseURL)
	}
}

func TestResolvePiProviderFallback(t *testing.T) {
	cfg, err := Resolve("", "", envMap(map[string]string{
		"PI_PROVIDER":           "cloudflare-workers-ai",
		"CLOUDFLARE_API_KEY":    "k",
		"CLOUDFLARE_ACCOUNT_ID": "acct",
	}))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Provider != "cloudflare-workers-ai" {
		t.Fatalf("Provider = %q", cfg.Provider)
	}
}

func TestResolveErrors(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		env      map[string]string
	}{
		{"workers ai missing key", "cloudflare-workers-ai", map[string]string{"CLOUDFLARE_ACCOUNT_ID": "a"}},
		{"workers ai missing account", "cloudflare-workers-ai", map[string]string{"CLOUDFLARE_API_KEY": "k"}},
		{"gateway missing gateway", "cloudflare-ai-gateway", map[string]string{"CLOUDFLARE_API_KEY": "k", "CLOUDFLARE_ACCOUNT_ID": "a"}},
		{"openai missing key", "", map[string]string{}},
		{"anthropic missing key", "anthropic", map[string]string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Resolve("", tc.provider, envMap(tc.env)); err == nil {
				t.Fatalf("expected resolution error")
			}
		})
	}
}

func TestRunWorkersAI(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"result":{"choices":[{"index":0,"message":{"role":"assistant","content":"hello back"},"finish_reason":"stop"}]},"success":true,"errors":[]}`)
	}))
	defer srv.Close()

	cfg := Config{Provider: "cloudflare-workers-ai", BaseURL: srv.URL, APIKey: "test-key", Model: "@cf/moonshotai/kimi-k2.6"}
	out, err := Run(cfg, "hi", srv.Client())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "hello back" {
		t.Fatalf("out = %q", out)
	}
	if gotPath != "/ai/run/@cf/moonshotai/kimi-k2.6" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("auth = %q", gotAuth)
	}
}

func TestRunOpenAIChat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()

	cfg := Config{Provider: "openai", BaseURL: srv.URL, APIKey: "sk", Model: "gpt-5"}
	out, err := Run(cfg, "ping", srv.Client())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "pong" {
		t.Fatalf("out = %q", out)
	}
}
