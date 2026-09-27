package modelfetch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/kiroauth"
	"github.com/vulnetix/belai/internal/kiromodels"
	"github.com/vulnetix/belai/internal/provider"
)

func TestListKiroMintsATokenAndRemembers(t *testing.T) {
	oidc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"accessToken":"kiro-access","expiresIn":3600}`)
	}))
	defer oidc.Close()
	prev := kiroTokens
	kiroTokens = kiroauth.NewRefresher(oidc.Client()).WithBaseURL(oidc.URL)
	defer func() { kiroTokens = prev }()
	kiromodels.Forget()
	defer kiromodels.Forget()

	var auth, profile string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, profile = r.Header.Get("Authorization"), r.URL.Query().Get("profileArn")
		if r.URL.Path != kiromodels.Path || r.URL.Query().Get("origin") != "AI_EDITOR" {
			t.Errorf("request = %s", r.URL)
		}
		fmt.Fprint(w, `{"models":[{"modelId":"claude-sonnet-4.5","modelName":"Claude Sonnet 4.5","tokenLimits":{"maxInputTokens":200000,"maxOutputTokens":64000},
		  "schema":{"properties":{"output_config":{"properties":{"effort":{"enum":["low","high"]}}}}}}]}`)
	}))
	defer api.Close()

	login := kiroauth.Login{RefreshToken: "rt", ClientID: "c", ClientSecret: "s", Region: "us-east-1", ProfileARN: "arn:aws:codewhisperer:us-east-1:1:profile/P"}.Encode()
	target := Target{Name: "kiro", BaseURL: api.URL, APIKey: login, Auth: provider.AuthKiro}
	if ep, _ := EndpointFor(target); !strings.HasSuffix(ep, "/ListAvailableModels?origin=AI_EDITOR") {
		t.Fatalf("endpoint = %q", ep)
	}
	got, err := List(context.Background(), target, api.Client())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if auth != "Bearer kiro-access" || profile != "arn:aws:codewhisperer:us-east-1:1:profile/P" {
		t.Fatalf("auth=%q profile=%q (the stored login must never be the bearer)", auth, profile)
	}
	if len(got) != 1 || got[0].ID != "claude-sonnet-4.5" || got[0].ContextWindow != 200000 || got[0].MaxOutput != 64000 || len(got[0].Efforts) != 2 {
		t.Fatalf("models = %+v", got)
	}
	if info, found, _ := kiromodels.Lookup(api.URL, "claude-sonnet-4.5"); !found || info.EffortBranch != kiromodels.BranchOutputConfig {
		t.Fatalf("catalogue not remembered: %+v", info)
	}
}
