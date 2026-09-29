package run

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/decisions"
)

func TestClassifierKindDerivesJev(t *testing.T) {
	cases := []struct {
		name string
		cls  *config.ClassifierSettings
		want string
	}{
		{"explicit jev", &config.ClassifierSettings{Kind: "jev"}, "jev"},
		{"legacy jev on openrouter reads as openrouter-decisions", &config.ClassifierSettings{Kind: "jev", Provider: "openrouter", Model: "typesafe/jev-1.13"}, "openrouter-decisions"},
		{"legacy jev on the local model reads as openrouter-decisions", &config.ClassifierSettings{Kind: "jev", Provider: decisions.LocalProvider}, "openrouter-decisions"},
		{"jev on typesafe stays jev", &config.ClassifierSettings{Kind: "jev", Provider: decisions.TypeSafeProvider}, "jev"},
		{"llm with typesafe reads as jev", &config.ClassifierSettings{Kind: "llm", Provider: decisions.TypeSafeProvider}, "jev"},
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
