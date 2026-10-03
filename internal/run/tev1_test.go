package run

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/decisions"
)

func TestResolveClassifierTev1OnTogether(t *testing.T) {
	cls := &config.ClassifierSettings{Provider: "together", Model: decisions.Tev1HostedModel}
	cc, err := ResolveClassifier(mainCfg("https://x.invalid"), cls, fakeSource{vals: map[string]string{"together:api_key": "tg-key"}})
	if err != nil {
		t.Fatal(err)
	}
	d := cc.Decisions
	if d.Backend != decisions.BackendChatLetters || d.BaseURL != "https://api.together.xyz/v1" || d.Model != decisions.Tev1HostedModel {
		t.Fatalf("decisions = %+v", d)
	}
	if k, err := d.Key(); err != nil || k != "tg-key" {
		t.Fatalf("key = %q, %v", k, err)
	}
	if _, ok := d.NewDecider(nil).(*decisions.ChatLetters); !ok {
		t.Fatal("Tev1 on Together must use the chat letter transport")
	}
	if ClassifierKind(cls) != ClassifierKindSystemOne {
		t.Error("Tev1 must read as a decision kind")
	}
	if !isDecisionsTarget("together", "", decisions.Tev1HostedModel) || isDecisionsTarget("together", "", "meta-llama/Llama-3.3-70B-Instruct-Turbo") {
		t.Error("Tev1 must never chat, and a Together chat model is not a decision backend")
	}
}

func TestResolveClassifierTev1FailsClosed(t *testing.T) {
	cls := &config.ClassifierSettings{Provider: "together", Model: decisions.Tev1HostedModel}
	cc, err := ResolveClassifier(mainCfg("https://x.invalid"), cls, fakeSource{vals: map[string]string{}})
	if err == nil {
		if _, kerr := cc.Decisions.Key(); kerr == nil {
			t.Fatal("no Together key must not resolve a key")
		}
	}
	for _, base := range []string{"http://api.example.com/v1", "https://user:pw@api.together.xyz/v1"} {
		_, err := ResolveClassifier(mainCfg("https://x.invalid"), cls, fakeSource{vals: map[string]string{"together:api_key": "k", "together:base_url": base}})
		if err == nil {
			t.Errorf("base_url %q must be refused", base)
		}
	}
}

func TestResolveClassifierTev1OnOllama(t *testing.T) {
	cls := &config.ClassifierSettings{Provider: "ollama", Model: "Tev1:4B"}
	cc, err := ResolveClassifier(mainCfg("https://x.invalid"), cls, fakeSource{vals: map[string]string{"ollama:host": "127.0.0.1", "ollama:port": "11500"}})
	if err != nil {
		t.Fatal(err)
	}
	d := cc.Decisions
	if d.Backend != decisions.BackendSystemOne || d.BaseURL != "http://127.0.0.1:11500" || d.Path != decisions.DefaultSystemOnePath ||
		d.Model != "tev1:4b" || !d.SendModel || d.MaxOptions != decisions.Tev1MaxOptions || d.MaxBodyBytes != 64<<10 {
		t.Fatalf("decisions = %+v", d)
	}
	_, err = ResolveClassifier(mainCfg("https://x.invalid"), cls, fakeSource{vals: map[string]string{"ollama:host": "ollama.example.com"}})
	if err == nil || !strings.Contains(err.Error(), "ollama") {
		t.Fatalf("plain http off loopback must be refused: %v", err)
	}
}
