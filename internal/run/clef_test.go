package run

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/rolemanager/jev"
)

const cfAcct = "0123456789abcdef0123456789abcdef"

func cfSource(extra map[string]string) fakeSource {
	vals := map[string]string{
		"cloudflare-workers-ai:api_key":    "cf-token",
		"cloudflare-workers-ai:account_id": cfAcct,
	}
	for k, v := range extra {
		vals[k] = v
	}
	return fakeSource{vals: vals}
}

func TestResolveClassifierClefOnWorkersAI(t *testing.T) {
	cc, err := ResolveClassifier(mainCfg("https://x.invalid"), &config.ClassifierSettings{Provider: "cloudflare-workers-ai", Model: "@cf/cloudflare/clef-flash"}, cfSource(nil))
	if err != nil {
		t.Fatal(err)
	}
	d := cc.Decisions
	if d.Backend != decisions.BackendSystemOne || d.BaseURL != "https://api.cloudflare.com/client/v4/accounts/"+cfAcct || d.Path != "/ai/run/@cf/cloudflare/clef-flash" ||
		d.WireModel != "clef-flash" || !d.SendModel || !d.Envelope || !d.CriteriaObject || d.MaxQuestions != 64 || d.MaxOptions != 255 {
		t.Fatalf("decisions = %+v", d)
	}
	if k, err := d.Key(); err != nil || k != "cf-token" {
		t.Fatalf("key = %q, %v", k, err)
	}
	if cc.Provider != "openai" {
		t.Errorf("chat fallback = %s", cc.Provider)
	}
	if ClassifierKind(&config.ClassifierSettings{Provider: "cloudflare-workers-ai", Model: "@cf/cloudflare/clef"}) != ClassifierKindSystemOne {
		t.Error("hosted Clef must read as kind systemone")
	}
	if !isDecisionsTarget("cloudflare-workers-ai", "", "@cf/cloudflare/clef") || isDecisionsTarget("cloudflare-workers-ai", "", "@cf/moonshotai/kimi-k2.6") {
		t.Error("Clef must never chat, and a Workers AI chat model is not a decision backend")
	}
}

func TestResolveClassifierClefFailsClosed(t *testing.T) {
	for name, src := range map[string]fakeSource{
		"no account":       {vals: map[string]string{"cloudflare-workers-ai:api_key": "k"}},
		"account not hex":  cfSource(map[string]string{"cloudflare-workers-ai:account_id": "../evil"}),
		"gateway off host": cfSource(map[string]string{"cloudflare-ai-gateway:base_url": "https://evil.example/v1/" + cfAcct + "/gw/compat"}),
		"gateway path":     cfSource(map[string]string{"cloudflare-ai-gateway:base_url": "https://gateway.ai.cloudflare.com/compat"}),
	} {
		prov := "cloudflare-workers-ai"
		if strings.HasPrefix(name, "gateway") {
			prov = "cloudflare-ai-gateway"
		}
		if _, err := ResolveClassifier(mainCfg("x"), &config.ClassifierSettings{Provider: prov, Model: "@cf/cloudflare/clef"}, src); err == nil {
			t.Errorf("%s: resolved", name)
		}
	}
}

func TestResolveClassifierClefThroughAIGateway(t *testing.T) {
	src := cfSource(map[string]string{
		"cloudflare-ai-gateway:token":    "gw-token",
		"cloudflare-ai-gateway:base_url": "https://gateway.ai.cloudflare.com/v1/" + cfAcct + "/team-gw/compat",
	})
	cc, err := ResolveClassifier(mainCfg("x"), &config.ClassifierSettings{Provider: "cloudflare-ai-gateway", Model: "@cf/cloudflare/clef"}, src)
	if err != nil {
		t.Fatal(err)
	}
	d := cc.Decisions
	if d.BaseURL != "https://gateway.ai.cloudflare.com/v1/"+cfAcct+"/team-gw" || d.Path != "/workers-ai/@cf/cloudflare/clef" || d.WireModel != "clef" {
		t.Fatalf("decisions = %+v", d)
	}
	h, _ := d.ExtraHeaders()
	if h["cf-aig-authorization"] != "Bearer gw-token" {
		t.Errorf("headers = %v", h)
	}
	// With only the account, the account's default gateway.
	cc, err = ResolveClassifier(mainCfg("x"), &config.ClassifierSettings{Provider: "cloudflare-ai-gateway", Model: "@cf/cloudflare/clef"}, cfSource(map[string]string{"cloudflare-ai-gateway:account_id": cfAcct}))
	if err != nil || cc.Decisions.BaseURL != "https://gateway.ai.cloudflare.com/v1/"+cfAcct+"/default" {
		t.Fatalf("default gateway: %+v, %v", cc.Decisions, err)
	}
}

// Selecting Clef is all it takes: the security guard, mode detection, routing
// and the Jev jobs ask it, through Cloudflare's envelope, with the short
// model name the API requires.
func TestClefServesEveryJevConsumer(t *testing.T) {
	var calls, chatCalls atomic.Int32
	cf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			Model     string                    `json:"model"`
			Questions map[string]map[string]any `json:"questions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Model != "clef-flash" || r.Header.Get("Authorization") != "Bearer cf-token" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":7001,"message":"model: must match clef|clef-flash"}]}`))
			return
		}
		answers := map[string]any{}
		for id, q := range body.Questions {
			if q["type"] == "choice" {
				crit, _ := q["criteria"].(map[string]any)
				probs := map[string]float64{}
				for k := range crit {
					probs[k] = 1 / float64(len(crit))
				}
				answers[id] = map[string]any{"type": "choice", "probabilities": probs}
				continue
			}
			answers[id] = map[string]any{"type": "noul", "noul": 0.01}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"model": "clef-flash", "answers": answers}, "success": true, "errors": []any{}})
	}))
	defer cf.Close()
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chatCalls.Add(1)
		chatCompletionEchoingModel()(w, r)
	}))
	defer chat.Close()

	cfg := mainCfg(chat.URL)
	cc, err := ResolveClassifier(cfg, &config.ClassifierSettings{Kind: "systemone", Provider: "cloudflare-workers-ai", Model: "@cf/cloudflare/clef-flash"}, cfSource(nil))
	if err != nil {
		t.Fatal(err)
	}
	cc.Decisions.BaseURL = cf.URL // the test server stands in for api.cloudflare.com
	cfg.Classifier = cc

	g := securityGuard(cfg, cf.Client(), nil)
	if _, ok := g.(*jev.Security); !ok {
		t.Fatalf("guard = %T", g)
	}
	got, err := g.Classify(context.Background(), rolemanager.ClassifierPayload{User: "ls output", Categories: []rolemanager.Sentinel{rolemanager.SentinelPromptInjection}})
	if err != nil || got != string(rolemanager.SentinelSafe) || calls.Load() != 1 || chatCalls.Load() != 0 {
		t.Fatalf("verdict %q err %v clef %d chat %d", got, err, calls.Load(), chatCalls.Load())
	}
	if NewModeDetector(cfg) == nil || NewJevJobs(cfg, nil) == nil {
		t.Fatal("mode detection and the Jev jobs must run on Clef")
	}
	cfg.Routing = RoutingConfig{Kind: config.RoutingRouted, Candidates: []RoutingCandidate{{Key: "mode_eval", Cfg: cfg}}}
	if r := newRoutedClassifier(cfg, cf.Client(), nil); r.jev == nil || r.jev.Identity() != "cloudflare-workers-ai/@cf/cloudflare/clef-flash" {
		t.Fatalf("routing jev = %v", r.jev)
	}
}
