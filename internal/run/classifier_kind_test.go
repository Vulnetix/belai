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
		{"llm with an openrouter jev model reads as jev", &config.ClassifierSettings{Kind: "llm", Provider: "openrouter", Model: "typesafe/jev-1.13"}, "jev"},
		{"llm with the local decision model reads as jev", &config.ClassifierSettings{Kind: "llm", Provider: decisions.LocalProvider}, "jev"},
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
