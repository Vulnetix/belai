package run

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/provider"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/rolemanager/jev"
)

func mainCfg(chatURL string) Config {
	return Config{Provider: "openai", BaseURL: chatURL, APIKey: "main-key", Model: "gpt-5"}
}

func TestResolveClassifierLocalDecisionModel(t *testing.T) {
	cc, err := ResolveClassifier(mainCfg("https://x.invalid"), &config.ClassifierSettings{
		Provider: decisions.LocalProvider, Model: "decider-4b",
		Decision: config.ClassifierDecisionSettings{TimeoutMS: 9000, MaxStateBytes: 4096},
	}, fakeSource{})
	if err != nil {
		t.Fatal(err)
	}
	if !cc.Decisions.On() || cc.Decisions.Backend != decisions.BackendLocal || cc.Decisions.Local.ID != "decider-4b" {
		t.Fatalf("decisions = %+v", cc.Decisions)
	}
	// The chat fields stay on the main model: it is the fallback, and the
	// decision provider is never prepared as a chat provider.
	if cc.Provider != "openai" || cc.Model != "gpt-5" || cc.APIKey != "main-key" {
		t.Fatalf("chat fallback = %s/%s", cc.Provider, cc.Model)
	}
	if cc.Decisions.Timeout.Milliseconds() != 9000 || cc.Decisions.MaxStateBytes != 4096 {
		t.Fatalf("knobs = %+v", cc.Decisions)
	}
}

func TestResolveClassifierRejectsUnknownLocalModel(t *testing.T) {
	_, err := ResolveClassifier(mainCfg("x"), &config.ClassifierSettings{Provider: decisions.LocalProvider, Model: "gpt-5"}, fakeSource{})
	if err == nil {
		t.Fatal("an unknown local decision model must fail closed")
	}
}

func jevProfileSource(base string) fakeProfileSource {
	return fakeProfileSource{
		vals:     map[string]string{"home-jev:api_key": "sk-home"},
		profiles: map[string]provider.Profile{"home-jev": {BaseURL: base, Kind: "jev", Models: []string{"laya"}, DecisionPath: "/systemone"}},
	}
}

func TestResolveClassifierSelfHostedJev(t *testing.T) {
	cc, err := ResolveClassifier(mainCfg("x"), &config.ClassifierSettings{Provider: "home-jev"}, jevProfileSource("https://jev.example"))
	if err != nil {
		t.Fatal(err)
	}
	d := cc.Decisions
	if d.Backend != decisions.BackendSystemOne || d.BaseURL != "https://jev.example" || d.Path != "/systemone" || d.Model != "laya" {
		t.Fatalf("decisions = %+v", d)
	}
	if k, _ := d.Key(); k != "sk-home" {
		t.Fatalf("key %q", k)
	}
	if _, err := ResolveClassifier(mainCfg("x"), &config.ClassifierSettings{Provider: "home-jev"}, jevProfileSource("http://jev.example")); err == nil {
		t.Fatal("plain http to a remote host must be refused")
	}
}

// systemOneStub answers every question with p and counts calls.
func systemOneStub(t *testing.T, p float64, calls *atomic.Int32) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			Questions map[string]map[string]any `json:"questions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		answers := map[string]any{}
		for id, q := range body.Questions {
			if q["type"] == "choice" {
				answers[id] = map[string]any{"type": "choice", "probabilities": map[string]float64{}}
				continue
			}
			answers[id] = map[string]any{"type": "noul", "noul": p}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"answers": answers})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSelfHostedJevServesSecurityModeAndRouting(t *testing.T) {
	var jevCalls, chatCalls atomic.Int32
	jevSrv := systemOneStub(t, 0.01, &jevCalls)
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chatCalls.Add(1)
		chatCompletionEchoingModel()(w, r)
	}))
	defer chat.Close()

	cfg := mainCfg(chat.URL)
	cc, err := ResolveClassifier(cfg, &config.ClassifierSettings{Provider: "home-jev"}, jevProfileSource(jevSrv.URL))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Classifier = cc

	// Security: the self-hosted backend answers; all-low is SAFE with no
	// chat call.
	g := securityGuard(cfg, jevSrv.Client(), nil)
	if _, ok := g.(*jev.Security); !ok {
		t.Fatalf("guard = %T", g)
	}
	got, err := g.Classify(context.Background(), rolemanager.ClassifierPayload{User: "ls output", Categories: []rolemanager.Sentinel{rolemanager.SentinelPromptInjection}})
	if err != nil || got != string(rolemanager.SentinelSafe) || jevCalls.Load() != 1 || chatCalls.Load() != 0 {
		t.Fatalf("verdict %q err %v jev %d chat %d", got, err, jevCalls.Load(), chatCalls.Load())
	}

	// Mode detection: served without an OpenRouter key and without routed.
	if cfg.Routing.JevToken != nil || cfg.Routing.Kind == config.RoutingRouted {
		t.Fatal("test precondition: no Jev token, not routed")
	}
	det := NewModeDetector(cfg)
	if det == nil {
		t.Fatal("mode detection must use the self-hosted backend")
	}

	// Routing: the routed classifier asks the same backend.
	cfg.Routing = RoutingConfig{Kind: config.RoutingRouted, Candidates: []RoutingCandidate{{Key: "mode_eval", Cfg: cfg}}}
	r := newRoutedClassifier(cfg, jevSrv.Client(), nil)
	if r.jev == nil || r.jev.Identity() != "home-jev/laya" {
		t.Fatalf("routing jev = %v", r.jev)
	}
}

func TestDecisionProviderNeverChats(t *testing.T) {
	if !isDecisionsTarget(decisions.LocalProvider, "", "decider-4b") || !isDecisionsTarget("home", "jev", "") || !isDecisionsTarget("openrouter", "", "typesafe/jev-1.13") {
		t.Fatal("decision targets not recognised")
	}
	if isDecisionsTarget("openai", "", "gpt-5") {
		t.Fatal("a chat model misread as a decision backend")
	}
}

// A typesafe/jev* model with no provider inherits the main provider, which
// cannot serve it; it must fall back to the main model instead of being sent
// to that provider as a chat model (which the provider rejects every time).
func TestJevModelOnAnotherProviderFallsBackToMain(t *testing.T) {
	main := Config{Provider: "cloudflare-ai-gateway", BaseURL: "https://gw.invalid", APIKey: "k", Model: "@cf/deepseek-ai/deepseek-v4-pro-0813"}
	cc, err := ResolveClassifier(main, &config.ClassifierSettings{Model: "typesafe/jev-1.13"}, fakeSource{})
	if err != nil {
		t.Fatal(err)
	}
	if cc.Provider != "cloudflare-ai-gateway" || cc.Model != main.Model {
		t.Fatalf("classifier = %s/%s, want the main model", cc.Provider, cc.Model)
	}
	if isDecisionsTarget(cc.Provider, "", cc.Model) {
		t.Fatal("the fallback must be a chat model")
	}
	if classifierModelApplies("openrouter", "typesafe/jev-1.13") != true || classifierModelApplies("openai", "typesafe/jev-1.13") {
		t.Fatal("jev applies to openrouter only")
	}
}
