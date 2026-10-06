package run

import (
	"errors"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/firewall"
)

func TestClassifierKindDerivesSystemOne(t *testing.T) {
	cases := []struct {
		name string
		cls  *config.ClassifierSettings
		want string
	}{
		{"explicit systemone", &config.ClassifierSettings{Kind: "systemone"}, "systemone"},
		{"legacy jev reads as systemone", &config.ClassifierSettings{Kind: "jev"}, "systemone"},
		{"legacy jev on openrouter reads as openrouter-decisions", &config.ClassifierSettings{Kind: "jev", Provider: "openrouter", Model: "typesafe/jev-1.13"}, "openrouter-decisions"},
		{"legacy jev on the local model reads as openrouter-decisions", &config.ClassifierSettings{Kind: "jev", Provider: decisions.LocalProvider}, "openrouter-decisions"},
		{"legacy jev on typesafe reads as systemone", &config.ClassifierSettings{Kind: "jev", Provider: decisions.TypeSafeProvider}, "systemone"},
		{"llm with typesafe reads as systemone", &config.ClassifierSettings{Kind: "llm", Provider: decisions.TypeSafeProvider}, "systemone"},
		{"no kind with strands-decider reads as systemone", &config.ClassifierSettings{Provider: decisions.DeciderProvider, Model: "decider-2b"}, "systemone"},
		{"llm with an openrouter decider model reads as openrouter-decisions", &config.ClassifierSettings{Kind: "llm", Provider: "openrouter", Model: "strandsagents/strands-decider-2b"}, "openrouter-decisions"},
		{"llm with an openrouter jev model reads as openrouter-decisions", &config.ClassifierSettings{Kind: "llm", Provider: "openrouter", Model: "typesafe/jev-1.13"}, "openrouter-decisions"},
		{"llm with the local decision model reads as openrouter-decisions", &config.ClassifierSettings{Kind: "llm", Provider: decisions.LocalProvider}, "openrouter-decisions"},
		{"llm with a chat model stays llm", &config.ClassifierSettings{Kind: "llm", Provider: "openrouter", Model: "openai/gpt-5"}, "llm"},
		{"models is never rewritten", &config.ClassifierSettings{Kind: "models", Provider: "openrouter", Model: "typesafe/jev-1.13"}, "models"},
	}
	for _, c := range cases {
		if got := ClassifierKind(c.cls); got != c.want {
			t.Errorf("%s: kind = %q, want %q", c.name, got, c.want)
		}
	}
}

// Kind jev is a decision backend and nothing else: a chat selection is refused
// instead of being classified by a model nobody asked for.
func TestResolveClassifierKindJevRefusesChat(t *testing.T) {
	main := Config{Provider: "openrouter", Model: "openai/gpt-5"}
	_, err := ResolveClassifier(main, &config.ClassifierSettings{Kind: "jev", Provider: "openrouter", Model: "openai/gpt-5"}, EnvSource(func(string) string { return "k" }))
	if err == nil || !strings.Contains(err.Error(), `kind "jev"`) {
		t.Fatalf("err = %v, want a kind jev refusal", err)
	}
	if _, err := ResolveClassifier(main, &config.ClassifierSettings{Kind: "jev"}, EnvSource(func(string) string { return "k" })); err == nil {
		t.Fatal("kind jev with no selection resolved")
	}
	if _, err := ResolveClassifier(main, &config.ClassifierSettings{Kind: "jev", Provider: "openrouter", Model: "typesafe/jev-1.13"}, EnvSource(func(string) string { return "k" })); err != nil {
		t.Fatalf("jev on openrouter: %v", err)
	}
}

// TypeSafe resolves to the hosted native API: a fixed origin, its default
// model, and a key that is required.
func TestResolveClassifierTypeSafe(t *testing.T) {
	main := Config{Provider: "openrouter", Model: "openai/gpt-5"}
	cls := &config.ClassifierSettings{Kind: "jev", Provider: decisions.TypeSafeProvider}
	cc, err := ResolveClassifier(main, cls, EnvSource(func(k string) string {
		if k == decisions.TypeSafeKeyEnv {
			return " ts-key "
		}
		return ""
	}))
	if err != nil {
		t.Fatal(err)
	}
	d := cc.Decisions
	if d.Backend != decisions.BackendSystemOne || d.BaseURL != decisions.TypeSafeBaseURL || d.Path != decisions.DefaultSystemOnePath || d.Model != "jev-latest" || !d.SendModel {
		t.Fatalf("decisions = %+v", d)
	}
	if k, err := d.Key(); err != nil || k != "ts-key" {
		t.Fatalf("key = %q, %v", k, err)
	}
	// No key: the lookup fails instead of sending an anonymous request.
	cc, err = ResolveClassifier(main, cls, EnvSource(func(string) string { return "" }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cc.Decisions.Key(); err == nil {
		t.Fatal("a missing TYPESAFE_API_KEY was accepted")
	}
}

// OpenRouter's Decisions API is a decision backend: with an AI Firewall
// routing openrouter, the classifier still resolves to the user's own
// OpenRouter key and no route, so the direct Decisions call never carries the
// firewall's credential (and /model's test asks what the runtime asks).
func TestResolveClassifierDecisionsModelIsNeverFirewallRouted(t *testing.T) {
	src := &fakeFirewallSource{
		values:   map[string]string{"openrouter:api_key": "or-own-key"},
		firewall: true,
		gateway:  "https://guardrails.vulnetix.com",
		org:      "org-1",
		apiKey:   "vulnetix-key",
		routable: map[string]bool{"openrouter": true, "cloudflare-workers-ai": true},
	}
	main := Config{Provider: "cloudflare-workers-ai", Model: "@cf/x"}
	cls := &config.ClassifierSettings{Kind: ClassifierKindOpenRouterDecisions, Provider: "openrouter", Model: "typesafe/jev-1.13"}
	cc, err := ResolveClassifier(main, cls, src)
	if err != nil {
		t.Fatal(err)
	}
	if cc.Firewall != nil || cc.APIKey != "or-own-key" || strings.Contains(cc.BaseURL, "vulnetix") {
		t.Fatalf("classifier = firewall %v key %q base %q, want own key and no route", cc.Firewall, cc.APIKey, cc.BaseURL)
	}
	// The same holds when openrouter is also the main provider.
	main = Config{Provider: "openrouter", Model: "openai/gpt-5", APIKey: "vulnetix-key", Firewall: &firewall.Route{Instance: "vulnetix"}}
	if cc, err = ResolveClassifier(main, cls, src); err != nil || cc.Firewall != nil || cc.APIKey != "or-own-key" {
		t.Fatalf("same-provider: %+v %v", cc, err)
	}
	// A chat model on openrouter is still routed as before.
	chat := &config.ClassifierSettings{Kind: "llm", Provider: "openrouter", Model: "openai/gpt-5"}
	if cc, err = ResolveClassifier(Config{Provider: "cloudflare-workers-ai"}, chat, src); err != nil || cc.Firewall == nil {
		t.Fatalf("chat classifier lost its firewall route: %+v %v", cc, err)
	}
	// No key of the user's own: an error that says what to set, never the
	// firewall's key.
	src.values = nil
	if _, err = ResolveClassifier(Config{Provider: "cloudflare-workers-ai"}, cls, src); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v, want ErrNotConfigured", err)
	}
}
