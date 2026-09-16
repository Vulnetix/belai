package run

import (
	"testing"

	"github.com/vulnetix/signet/internal/wire"
)

func TestDetectToolMethodBuiltins(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want ToolMethod
	}{
		{"anthropic", Config{Provider: "anthropic"}, ToolMethodBlocks},
		{"cloudflare-workers-ai", Config{Provider: "cloudflare-workers-ai"}, ToolMethodObject},
		{"cloudflare-ai-gateway-claude", Config{Provider: "cloudflare-ai-gateway", Model: "claude-sonnet-4-5"}, ToolMethodBlocks},
		{"cloudflare-ai-gateway-openai", Config{Provider: "cloudflare-ai-gateway", Model: "gpt-5"}, ToolMethodString},
		{"openai", Config{Provider: "openai"}, ToolMethodString},
		{"openrouter", Config{Provider: "openrouter"}, ToolMethodString},
		{"google-gemini", Config{Provider: "google-gemini"}, ToolMethodString},
		{"ollama", Config{Provider: "ollama"}, ToolMethodString},
		{"github-copilot", Config{Provider: "github-copilot"}, ToolMethodString},
		{"huggingface", Config{Provider: "huggingface"}, ToolMethodString},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DetectToolMethod(tc.cfg)
			if err != nil {
				t.Fatalf("DetectToolMethod: %v", err)
			}
			if got != tc.want {
				t.Fatalf("DetectToolMethod = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDetectToolMethodExplicitWins(t *testing.T) {
	cfg := Config{Provider: "openai", ToolMethod: ToolMethodObject}
	got, err := DetectToolMethod(cfg)
	if err != nil {
		t.Fatalf("DetectToolMethod: %v", err)
	}
	if got != ToolMethodObject {
		t.Fatalf("explicit ToolMethod should win, got %v", got)
	}
}

func TestDetectToolMethodUnknownProviderErrors(t *testing.T) {
	_, err := DetectToolMethod(Config{Provider: "gemini"})
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestDetectToolMethodCustomSurface(t *testing.T) {
	cases := []struct {
		api  wire.Surface
		want ToolMethod
	}{
		{wire.SurfaceAnthropicMessages, ToolMethodBlocks},
		{wire.SurfaceOpenAIChat, ToolMethodString},
	}
	for _, tc := range cases {
		t.Run(string(tc.api), func(t *testing.T) {
			got, err := DetectToolMethod(Config{Provider: "my-llm", API: tc.api})
			if err != nil {
				t.Fatalf("DetectToolMethod: %v", err)
			}
			if got != tc.want {
				t.Fatalf("DetectToolMethod = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDetectToolMethodCustomUnknownSurfaceErrors(t *testing.T) {
	_, err := DetectToolMethod(Config{Provider: "my-llm", API: wire.Surface("bogus")})
	if err == nil {
		t.Fatal("expected error for unknown custom surface")
	}
}
