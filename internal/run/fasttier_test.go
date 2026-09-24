package run

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/rolemanager"
)

func TestResolveFastDefaultsToTheProviderFastModel(t *testing.T) {
	main := Config{Provider: "anthropic", BaseURL: "https://api.anthropic.com", APIKey: "k", Model: "claude-opus-4-5"}
	rc, err := ResolveRouting(main, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rc.Fast == nil || rc.Fast.Model != "claude-haiku-4-5" || rc.Fast.APIKey != "k" {
		t.Fatalf("fast = %+v, want the registry fast model on the main credentials", rc.Fast)
	}
	if rc.Kind != config.RoutingDefined {
		t.Fatalf("kind = %q", rc.Kind)
	}
}

func TestResolveFastIsAbsentWhenItIsTheMainModel(t *testing.T) {
	main := Config{Provider: "anthropic", APIKey: "k", Model: "claude-haiku-4-5"}
	if rc, _ := ResolveRouting(main, nil, nil); rc.Fast != nil {
		t.Fatalf("fast = %+v, want nil when it equals the main model", rc.Fast)
	}
	noFast := Config{Provider: "openrouter", APIKey: "k", Model: "openrouter/free"}
	if rc, _ := ResolveRouting(noFast, nil, nil); rc.Fast != nil {
		t.Fatalf("a provider without a fast model has no fast tier: %+v", rc.Fast)
	}
}

func TestResolveFastHonoursAnExplicitSetting(t *testing.T) {
	main := Config{Provider: "anthropic", APIKey: "k", Model: "claude-opus-4-5"}
	src := EnvSource(func(k string) string {
		if k == "OPENAI_API_KEY" {
			return "sk-o"
		}
		return ""
	})
	rs := &config.RoutingSettings{Fast: &config.RoutingTarget{Provider: "openai", Model: "gpt-5-mini"}}
	rc, err := ResolveRouting(main, rs, src)
	if err != nil {
		t.Fatal(err)
	}
	if rc.Fast == nil || rc.Fast.Provider != "openai" || rc.Fast.APIKey != "sk-o" {
		t.Fatalf("fast = %+v, want the cross-provider fast model", rc.Fast)
	}
	// An explicit fast model that cannot be configured is an error, not a
	// silent fall back to the main model.
	bad := &config.RoutingSettings{Fast: &config.RoutingTarget{Provider: "groq", Model: "x"}}
	if _, err := ResolveRouting(main, bad, EnvSource(func(string) string { return "" })); err == nil {
		t.Fatal("an unconfigured explicit fast model must fail")
	}
}

// roleServer records which model answered each system prompt.
func roleServer(t *testing.T) (*httptest.Server, func() map[string][]string) {
	t.Helper()
	var mu sync.Mutex
	seen := map[string][]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		seen[req.Messages[0].Content] = append(seen[req.Messages[0].Content], req.Model)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"SAFE"},"finish_reason":"stop"}]}`)
	}))
	return srv, func() map[string][]string {
		mu.Lock()
		defer mu.Unlock()
		return seen
	}
}

func TestFastRolesUseTheFastTierAndGenerativeRolesStayMain(t *testing.T) {
	srv, seen := roleServer(t)
	defer srv.Close()
	main := Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "gpt-5"}
	rc, err := ResolveRouting(main, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	main.Routing = rc
	c := NewRoleClassifier(main, srv.Client(), nil)
	for _, uc := range []string{rolemanager.UseCaseModeEval, rolemanager.UseCaseGoalEval, rolemanager.UseCaseCompaction, rolemanager.UseCaseGoalContract} {
		if _, err := c.Classify(context.Background(), rolemanager.ClassifierPayload{System: uc, User: "u", UseCase: uc}); err != nil {
			t.Fatal(err)
		}
	}
	got := seen()
	for uc, want := range map[string]string{
		rolemanager.UseCaseModeEval:     "gpt-5-mini",
		rolemanager.UseCaseGoalEval:     "gpt-5-mini",
		rolemanager.UseCaseCompaction:   "gpt-5",
		rolemanager.UseCaseGoalContract: "gpt-5-mini",
	} {
		if len(got[uc]) != 1 || got[uc][0] != want {
			t.Fatalf("%s answered by %v, want %s", uc, got[uc], want)
		}
	}
}

// TestRoutedGoalContractFallsBackToTheFastTier pins the goal contract to the
// fast tier when Jev does not settle it: the main model is a slow reasoning
// model often enough that the draft's deadline expired on it.
func TestRoutedGoalContractFallsBackToTheFastTier(t *testing.T) {
	srv, seen := roleServer(t)
	defer srv.Close()
	jevSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 400, not 5xx: the OpenRouter SDK retries a 5xx with backoff.
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer jevSrv.Close()

	main := Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "gpt-5"}
	rc, err := ResolveRouting(main, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rc.Kind = config.RoutingRouted
	rc.Candidates = []RoutingCandidate{{Key: "a", Cfg: Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "gpt-4.1"}}}
	rc.JevToken = func() (string, error) { return "test-key", nil }
	main.Routing = rc

	r := NewRoleClassifier(main, srv.Client(), nil).(*routedClassifier)
	r.jev.SetEndpoint(jevSrv.URL)
	for _, uc := range []string{rolemanager.UseCaseGoalContract, rolemanager.UseCaseCompaction} {
		if _, err := r.Classify(context.Background(), rolemanager.ClassifierPayload{System: uc, User: "u", UseCase: uc}); err != nil {
			t.Fatal(err)
		}
	}
	got := seen()
	if m := got[rolemanager.UseCaseGoalContract]; len(m) != 1 || m[0] != "gpt-5-mini" {
		t.Fatalf("goal contract answered by %v, want the fast tier", m)
	}
	if m := got[rolemanager.UseCaseCompaction]; len(m) != 1 || m[0] != "gpt-5" {
		t.Fatalf("compaction answered by %v, want the main model", m)
	}
}

func TestGuardStaysOnTheMainModelUnlessTierFast(t *testing.T) {
	main := Config{Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "k", Model: "gpt-5"}
	rc, _ := ResolveRouting(main, nil, nil)
	main.Routing = rc

	cc, _ := ResolveClassifier(main, nil, nil)
	main.Classifier = cc
	if g := GuardConfig(main); g.Model != "gpt-5" {
		t.Fatalf("default guard = %s, want the main model", g.Model)
	}

	cc, _ = ResolveClassifier(main, &config.ClassifierSettings{Tier: config.ClassifierTierFast}, nil)
	main.Classifier = cc
	if g := GuardConfig(main); g.Model != "gpt-5-mini" || g.Effort != "none" {
		t.Fatalf("tier fast guard = %+v, want the fast model with reasoning off", g)
	}

	// An explicit classifier model outranks the tier.
	cc, _ = ResolveClassifier(main, &config.ClassifierSettings{Tier: config.ClassifierTierFast, Model: "gpt-4.1"}, nil)
	main.Classifier = cc
	if g := GuardConfig(main); g.Model != "gpt-4.1" {
		t.Fatalf("explicit guard = %s, want gpt-4.1", g.Model)
	}
}
