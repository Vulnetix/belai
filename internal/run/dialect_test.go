package run

import "testing"

func TestResolveDialectBuiltins(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want dialect
	}{
		{
			name: "workers",
			cfg:  Config{Provider: "cloudflare-workers-ai"},
			want: dialect{kind: kindWorkersAI, route: routeWorkersAI},
		},
		{
			name: "gateway claude",
			cfg:  Config{Provider: "cloudflare-ai-gateway", Model: "claude-sonnet-4-5"},
			want: dialect{kind: kindAnthropicMessages, route: routeGateway},
		},
		{
			name: "gateway openai",
			cfg:  Config{Provider: "cloudflare-ai-gateway", Model: "gpt-5"},
			want: dialect{kind: kindOpenAIChat, route: routeGateway},
		},
		{
			name: "anthropic",
			cfg:  Config{Provider: "anthropic"},
			want: dialect{kind: kindAnthropicMessages, route: routeNative, thinking: true},
		},
		{
			name: "openai",
			cfg:  Config{Provider: "openai"},
			want: dialect{kind: kindOpenAIChat, route: routeNative, effort: true, usage: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveDialect(tc.cfg)
			if err != nil {
				t.Fatalf("resolveDialect: %v", err)
			}
			if got != tc.want {
				t.Fatalf("resolveDialect = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestResolveDialectGatewayClaudePrefixIsCaseInsensitive(t *testing.T) {
	d, err := resolveDialect(Config{Provider: "cloudflare-ai-gateway", Model: "CLAUDE-OPUS-4-5"})
	if err != nil {
		t.Fatalf("resolveDialect: %v", err)
	}
	if d.kind != kindAnthropicMessages {
		t.Fatalf("kind = %d, want kindAnthropicMessages", d.kind)
	}
}

func TestResolveDialectUnknownProviderErrors(t *testing.T) {
	if _, err := resolveDialect(Config{Provider: "gemini"}); err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestResolveDialectNewBuiltinsOpenAIChat(t *testing.T) {
	for _, name := range []string{"openrouter", "google-gemini", "ollama"} {
		t.Run(name, func(t *testing.T) {
			d, err := resolveDialect(Config{Provider: name})
			if err != nil {
				t.Fatalf("resolveDialect: %v", err)
			}
			if d.kind != kindOpenAIChat || d.route != routeNative {
				t.Fatalf("dialect = %+v, want openai chat native", d)
			}
			if d.effort || d.usage || d.thinking {
				t.Fatalf("feature bits should stay off for %s: %+v", name, d)
			}
		})
	}
}
