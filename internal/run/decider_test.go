package run

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/deciderserver"
	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/rolemanager/jev"
)

// deciderStub mimics strands-decider serve: /health names the checkpoint, and
// /v1/systemone refuses a choice whose criteria are not an object, as the
// real server does (HTTP 422).
func deciderStub(t *testing.T, p float64, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_, _ = w.Write([]byte(`{"status":"ok","model":"strands-decider-2b","checkpoint":"StrandsAgents/strands-decider-2B-hobson-v19","device":"cpu"}`))
			return
		}
		calls.Add(1)
		var body struct {
			Questions map[string]struct {
				Type     string          `json:"type"`
				Criteria json.RawMessage `json:"criteria"`
			} `json:"questions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		answers := map[string]any{}
		for id, q := range body.Questions {
			if q.Type == "choice" {
				var crit map[string]any
				if json.Unmarshal(q.Criteria, &crit) != nil {
					http.Error(w, `{"detail":"criteria must be an object"}`, http.StatusUnprocessableEntity)
					return
				}
				probs := map[string]float64{}
				first := ""
				for k := range crit {
					probs[k] = 0
					if first == "" || k < first {
						first = k
					}
				}
				probs[first] = 1
				answers[id] = map[string]any{"type": "choice", "choice": first, "probabilities": probs, "confidence": 1}
				continue
			}
			answers[id] = map[string]any{"type": "noul", "noul": p}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "strands-decider-2b", "answers": answers, "usage": map[string]int{"input_tokens": 9}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestResolveClassifierStrandsDecider(t *testing.T) {
	cc, err := ResolveClassifier(mainCfg("https://x.invalid"), &config.ClassifierSettings{Provider: decisions.DeciderProvider, Model: "decider-2b"}, fakeSource{})
	if err != nil {
		t.Fatal(err)
	}
	d := cc.Decisions
	if d.Backend != decisions.BackendSystemOne || d.Decider == nil || d.Decider.ID != "decider-2b" || !d.CriteriaObject || d.MaxOptions != 24 || d.Key != nil {
		t.Fatalf("decisions = %+v", d)
	}
	if d.Label() != "strands-decider/decider-2b" {
		t.Errorf("label = %q", d.Label())
	}
	// The chat fields stay on the main model, which answers what the decider
	// hands back.
	if cc.Provider != "openai" || cc.Model != "gpt-5" {
		t.Fatalf("chat fallback = %s/%s", cc.Provider, cc.Model)
	}
	if _, err := ResolveClassifier(mainCfg("x"), &config.ClassifierSettings{Provider: decisions.DeciderProvider, Model: "decider-4b"}, fakeSource{}); err == nil {
		t.Fatal("a model that is not a Strands Decider checkpoint must fail closed")
	}
	if ClassifierKind(&config.ClassifierSettings{Provider: decisions.DeciderProvider}) != ClassifierKindSystemOne {
		t.Error("strands-decider must read as kind systemone")
	}
}

// Selecting the decider is all it takes: the security guard, mode detection,
// routing and the Jev jobs all ask it, with no key and no other setting.
func TestStrandsDeciderServesEveryJevConsumer(t *testing.T) {
	var calls, chatCalls atomic.Int32
	srv := deciderStub(t, 0.01, &calls)
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chatCalls.Add(1)
		chatCompletionEchoingModel()(w, r)
	}))
	defer chat.Close()
	sup := deciderserver.Shared(decisions.Decider2B)
	sup.Adopt(&deciderserver.Handle{BaseURL: srv.URL})
	t.Cleanup(func() { sup.Adopt(nil) })

	cfg := mainCfg(chat.URL)
	cc, err := ResolveClassifier(cfg, &config.ClassifierSettings{Kind: "systemone", Provider: decisions.DeciderProvider, Model: "decider-2b"}, fakeSource{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Classifier = cc

	g := securityGuard(cfg, srv.Client(), nil)
	if _, ok := g.(*jev.Security); !ok {
		t.Fatalf("guard = %T", g)
	}
	got, err := g.Classify(context.Background(), rolemanager.ClassifierPayload{User: "ls output", Categories: []rolemanager.Sentinel{rolemanager.SentinelPromptInjection}})
	if err != nil || got != string(rolemanager.SentinelSafe) || calls.Load() != 1 || chatCalls.Load() != 0 {
		t.Fatalf("verdict %q err %v decider %d chat %d", got, err, calls.Load(), chatCalls.Load())
	}
	if NewModeDetector(cfg) == nil {
		t.Fatal("mode detection must use the decider")
	}
	if NewJevJobs(cfg, nil) == nil {
		t.Fatal("the Jev jobs must run on the decider")
	}
	cfg.Routing = RoutingConfig{Kind: config.RoutingRouted, Candidates: []RoutingCandidate{{Key: "mode_eval", Cfg: cfg}}}
	r := newRoutedClassifier(cfg, srv.Client(), nil)
	if r.jev == nil || r.jev.Identity() != "strands-decider/decider-2b" {
		t.Fatalf("routing jev = %v", r.jev)
	}

	// A choice with bare options reaches the server as an object, which is
	// the only form it takes.
	res, err := cc.Decisions.NewDecider(srv.Client()).Decide(context.Background(), decisions.Request{
		State:     "x",
		Questions: map[string]decisions.Question{"m": {Type: decisions.TypeChoice, Instructions: "Which mode?", Options: []string{"agent", "plan"}}},
	})
	if err != nil || res.Answers["m"].Choice != "agent" {
		t.Fatalf("choice: %+v, %v", res, err)
	}
}

// A provider profile serving Strands Decider elsewhere (another host, a
// Hugging Face Inference Endpoint) takes the decider's request shape too, and
// the kind's legacy name still resolves.
func TestSystemOneProfileServingTheDecider(t *testing.T) {
	src := jevProfileSource("https://decider.example")
	p := src.profiles["home-jev"]
	p.Kind, p.Models = "jev", []string{"strands-decider-2b"}
	src.profiles["home-jev"] = p
	cc, err := ResolveClassifier(mainCfg("x"), &config.ClassifierSettings{Provider: "home-jev"}, src)
	if err != nil {
		t.Fatal(err)
	}
	if d := cc.Decisions; d.Backend != decisions.BackendSystemOne || !d.CriteriaObject || d.MaxOptions != 24 || d.Decider != nil {
		t.Fatalf("decisions = %+v", d)
	}
}
