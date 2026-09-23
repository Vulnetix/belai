package run

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/rolemanager/jev"
	"github.com/vulnetix/signet/internal/tools"
)

// jevClassifierConfig returns a Config whose classifier is the Jev Decisions
// model, with the main chat model pointed at chatBaseURL.
func jevClassifierConfig(chatBaseURL string) Config {
	return Config{
		Provider: "openai", BaseURL: chatBaseURL, APIKey: "main-key", Model: "gpt-5",
		Classifier: ClassifierConfig{Provider: "openrouter", BaseURL: "https://openrouter.ai/api/v1", APIKey: "jev-key", Model: "typesafe/jev-1.13"},
	}
}

// decisionsResponse renders a Decisions reply where every category scores low
// (SAFE). It is the shape the OpenRouter SDK unmarshals.
func decisionsAllSafe() string {
	return `{"model":"typesafe/jev-1.13","answers":{` +
		`"PROMPT_INJECTION":{"type":"noul","noul":0.0},` +
		`"JAILBREAK":{"type":"noul","noul":0.0},` +
		`"DATA_EXTRACTION":{"type":"noul","noul":0.0},` +
		`"MODEL_EXTRACTION":{"type":"noul","noul":0.0}},"usage":{"input_tokens":1,"output_tokens":1}}`
}

// chatCompletion renders a minimal OpenAI chat completion whose content echoes
// the requested model, so tests can assert which model actually chatted.
func chatCompletionEchoingModel() func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		content := body.Model
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "c", "object": "chat.completion",
			"choices": []map[string]any{{"index": 0, "message": map[string]string{"role": "assistant", "content": content}, "finish_reason": "stop"}},
			"usage":   map[string]any{},
		})
	}
}

func TestSecurityGuardUsesJevForDecisionsModel(t *testing.T) {
	cfg := jevClassifierConfig("https://example.invalid")
	g := securityGuard(cfg, http.DefaultClient, nil)
	if _, ok := g.(*jev.Security); !ok {
		t.Fatalf("securityGuard = %T, want *jev.Security for a Jev classifier", g)
	}
}

func TestSecurityGuardUsesChatClassifierForNonJev(t *testing.T) {
	cfg := Config{
		Provider: "openai", BaseURL: "https://example.invalid", APIKey: "k", Model: "gpt-5",
		Classifier: ClassifierConfig{Provider: "openai", BaseURL: "https://example.invalid", APIKey: "k", Model: "gpt-5-mini"},
	}
	g := securityGuard(cfg, http.DefaultClient, nil)
	if _, ok := g.(*jev.Security); ok {
		t.Fatalf("securityGuard = %T, want a chat classifier for a non-Jev classifier", g)
	}
}

// TestNewClassifierWithRetryJevChatsWithMain pins the general-inference
// exception: a classifier config that is a Jev Decisions model never chats; it
// falls back to the main provider/model.
func TestNewClassifierWithRetryJevChatsWithMain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(chatCompletionEchoingModel()))
	defer srv.Close()

	cfg := jevClassifierConfig(srv.URL)
	c := NewClassifierWithRetry(cfg, srv.Client(), nil)
	got, err := c.Classify(context.Background(), rolemanager.ClassifierPayload{System: "s", User: "u"})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != "gpt-5" {
		t.Fatalf("Classify = %q, want gpt-5 (the main model, not Jev)", got)
	}
}

// TestNewPipelineLLMPathJevSecurity runs the full llm-path pipeline with a Jev
// classifier. Security must go to the Decisions API and never to chat.
func TestNewPipelineLLMPathJevSecurity(t *testing.T) {
	var decisionsCalls, chatCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/alpha/decisions" {
			decisionsCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(decisionsAllSafe()))
			return
		}
		// Any other path is a chat/completions request, which a Jev guard must
		// never send.
		chatCalls.Add(1)
		t.Errorf("unexpected chat request to %s", r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	old := jev.DefaultServerURL
	jev.DefaultServerURL = srv.URL
	t.Cleanup(func() { jev.DefaultServerURL = old })

	cfg := jevClassifierConfig(srv.URL)
	p := NewPipelineWithRetry(cfg, srv.Client(), nil, nil)

	dec, err := p.Process(context.Background(), tools.Result{Kind: tools.KindBash, Content: "echo hi"})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if dec.Sentinel != rolemanager.SentinelSafe || dec.Action != rolemanager.ActionProceed {
		t.Fatalf("Decision = %+v, want SAFE/proceed", dec)
	}
	if decisionsCalls.Load() != 1 {
		t.Fatalf("Decisions called %d times, want 1", decisionsCalls.Load())
	}
	if chatCalls.Load() != 0 {
		t.Fatalf("chat called %d times, want 0", chatCalls.Load())
	}
}

// TestRoutedClassifierJevWinnerFallsBackToMain pins the routing exception: a
// Jev Decisions model may be stored as a candidate, but a routed winner that
// is a Jev model resolves to the main classifier rather than ever chatting.
func TestRoutedClassifierJevWinnerFallsBackToMain(t *testing.T) {
	var jevCalls atomic.Int32
	jevSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jevCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"typesafe/jev-1.13","answers":{"a":{"type":"noul","noul":0.9},"b":{"type":"noul","noul":0.1}},"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer jevSrv.Close()

	modelSrv := httptest.NewServer(http.HandlerFunc(chatCompletionEchoingModel()))
	defer modelSrv.Close()

	cfg := Config{
		Provider: "openai", BaseURL: modelSrv.URL, APIKey: "k", Model: "gpt-5",
		Routing: RoutingConfig{
			Kind: config.RoutingRouted,
			Candidates: []RoutingCandidate{
				{Key: "a", Cfg: Config{Provider: "openrouter", BaseURL: modelSrv.URL, APIKey: "k", Model: "typesafe/jev-1.13"}},
				{Key: "b", Cfg: Config{Provider: "openai", BaseURL: modelSrv.URL, APIKey: "k", Model: "gpt-5-mini"}},
			},
			JevToken: func() (string, error) { return "test-key", nil },
		},
	}
	r := newRoutedClassifier(cfg, modelSrv.Client(), nil)
	r.jev.SetEndpoint(jevSrv.URL)

	got, err := r.Classify(context.Background(), rolemanager.ClassifierPayload{System: "s", User: "u", UseCase: rolemanager.UseCaseGoalEval})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != "gpt-5" {
		t.Fatalf("Classify = %q, want gpt-5 (the main model, not the Jev winner)", got)
	}
	if jevCalls.Load() != 1 {
		t.Fatalf("Jev called %d times, want 1", jevCalls.Load())
	}
}

// TestResolveRoutingAcceptsJevEntries pins that stored Jev routing targets
// still load at resolve time; the runtime fallback is the classifier's job.
func TestResolveRoutingAcceptsJevEntries(t *testing.T) {
	main := Config{Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "k", Model: "gpt-5"}
	rs := &config.RoutingSettings{
		Kind: config.RoutingRouted,
		UseCases: map[string]config.RoutingTarget{
			"mode_eval": {Provider: "openrouter", Model: "typesafe/jev-1.13"},
		},
	}
	src := fakeSource{vals: map[string]string{"openrouter:api_key": "jr-token"}}
	rc, err := ResolveRouting(main, rs, src)
	if err != nil {
		t.Fatalf("ResolveRouting must accept a stored Jev target: %v", err)
	}
	if len(rc.Candidates) != 1 || rc.Candidates[0].Cfg.Model != "typesafe/jev-1.13" {
		t.Fatalf("candidates = %+v", rc.Candidates)
	}
}
