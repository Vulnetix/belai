package provider

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/wire"
)

func readBody(t *testing.T, req *http.Request) map[string]any {
	t.Helper()
	data, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal body %s: %v", data, err)
	}
	return m
}

func TestOpenAIChatRequest(t *testing.T) {
	p, err := New("openai", "https://api.openai.com/v1", "sk-test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req, err := p.NewChatRequest(wire.OpenAIChatRequest{
		Model:    "gpt-5",
		Messages: []wire.OpenAIChatMessage{{Role: "user", Content: "hi"}},
		Stream:   false,
	})
	if err != nil {
		t.Fatalf("NewChatRequest: %v", err)
	}
	if got, want := req.URL.String(), "https://api.openai.com/v1/chat/completions"; got != want {
		t.Fatalf("URL = %q, want %q", got, want)
	}
	if got, want := req.Header.Get("Authorization"), "Bearer sk-test"; got != want {
		t.Fatalf("Authorization = %q, want %q", got, want)
	}
	if got := req.Header.Get("x-api-key"); got != "" {
		t.Fatalf("x-api-key should be empty for OpenAI, got %q", got)
	}
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}

	body := readBody(t, req)
	if body["model"] != "gpt-5" {
		t.Fatalf("body.model = %v", body["model"])
	}
	if _, ok := body["stream"]; ok {
		t.Fatalf("body.stream should be omitted when false, got %v", body["stream"])
	}
	msgs, ok := body["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("body.messages = %v", body["messages"])
	}
}

func TestOpenAIChatRequestStreaming(t *testing.T) {
	p, _ := New("openai", "https://gateway.example/v1", "sk")
	req, err := p.NewChatRequest(wire.OpenAIChatRequest{Model: "gpt-5", Stream: true})
	if err != nil {
		t.Fatalf("NewChatRequest: %v", err)
	}
	body := readBody(t, req)
	if body["stream"] != true {
		t.Fatalf("body.stream = %v, want true", body["stream"])
	}
}

func TestOpenAIResponsesRequest(t *testing.T) {
	p, _ := New("openai", "https://api.openai.com/v1/", "sk-test")
	req, err := p.NewResponsesRequest(wire.OpenAIResponsesRequest{
		Model: "gpt-5",
		Input: []wire.OpenAIChatMessage{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("NewResponsesRequest: %v", err)
	}
	// trailing slash on base URL must not produce a double slash
	if got, want := req.URL.String(), "https://api.openai.com/v1/responses"; got != want {
		t.Fatalf("URL = %q, want %q", got, want)
	}
	if got, want := req.Header.Get("Authorization"), "Bearer sk-test"; got != want {
		t.Fatalf("Authorization = %q, want %q", got, want)
	}
	body := readBody(t, req)
	if body["model"] != "gpt-5" {
		t.Fatalf("body.model = %v", body["model"])
	}
	if _, ok := body["input"]; !ok {
		t.Fatalf("body.input missing: %v", body)
	}
}

func TestAnthropicMessagesRequest(t *testing.T) {
	p, err := New("anthropic", "https://api.anthropic.com", "sk-ant-test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req, err := p.NewMessagesRequest(wire.AnthropicMessagesRequest{
		Model:     "claude-opus-4",
		MaxTokens: 4096,
		System:    "be brief",
		Messages:  []wire.AnthropicMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("NewMessagesRequest: %v", err)
	}
	if got, want := req.URL.String(), "https://api.anthropic.com/v1/messages"; got != want {
		t.Fatalf("URL = %q, want %q (no /v1 on the base URL)", got, want)
	}
	if got, want := req.Header.Get("x-api-key"), "sk-ant-test"; got != want {
		t.Fatalf("x-api-key = %q, want %q", got, want)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization should be empty for Anthropic, got %q", got)
	}
	if got, want := req.Header.Get("anthropic-version"), "2023-06-01"; got != want {
		t.Fatalf("anthropic-version = %q, want %q", got, want)
	}
	body := readBody(t, req)
	if body["model"] != "claude-opus-4" {
		t.Fatalf("body.model = %v", body["model"])
	}
	if body["max_tokens"] != float64(4096) {
		t.Fatalf("body.max_tokens = %v", body["max_tokens"])
	}
	if body["system"] != "be brief" {
		t.Fatalf("body.system = %v", body["system"])
	}
}

func TestNewValidation(t *testing.T) {
	cases := []struct {
		name    string
		prov    string
		baseURL string
		key     string
		wantErr string
	}{
		{"bad provider", "gemini", "https://x.example/v1", "k", "unsupported provider"},
		{"empty base", "openai", "", "k", "base_url is required"},
		{"bad scheme", "openai", "ftp://x.example/v1", "k", "invalid base_url"},
		{"empty key", "openai", "https://x.example/v1", "", "api_key is required"},
		{"anthropic ok", "anthropic", "https://x.example", "k", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.prov, tc.baseURL, tc.key)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

// TestLiveRequest only runs when SIGNET_LIVE_TEST is set; it proves a real
// request can be constructed and dispatched against a live endpoint.
func TestLiveRequest(t *testing.T) {
	if testing.Short() || os.Getenv("SIGNET_LIVE_TEST") == "" {
		t.Skip("live network test gated behind SIGNET_LIVE_TEST=1")
	}
	base := os.Getenv("SIGNET_LIVE_BASE_URL")
	key := os.Getenv("SIGNET_LIVE_API_KEY")
	if base == "" || key == "" {
		t.Skip("SIGNET_LIVE_BASE_URL and SIGNET_LIVE_API_KEY must be set")
	}
	prov := "openai"
	if os.Getenv("SIGNET_LIVE_PROVIDER") != "" {
		prov = os.Getenv("SIGNET_LIVE_PROVIDER")
	}
	p, err := New(prov, base, key)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var req *http.Request
	if p.IsAnthropic() {
		req, err = p.NewMessagesRequest(wire.AnthropicMessagesRequest{Model: "claude-opus-4", MaxTokens: 16, Messages: []wire.AnthropicMessage{{Role: "user", Content: "ping"}}})
	} else {
		req, err = p.NewChatRequest(wire.OpenAIChatRequest{Model: "gpt-4o-mini", Messages: []wire.OpenAIChatMessage{{Role: "user", Content: "ping"}}})
	}
	if err != nil {
		t.Fatalf("build live request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("live request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		t.Fatalf("live status = %d", resp.StatusCode)
	}
}
