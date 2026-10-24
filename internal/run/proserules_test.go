package run

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/prompt"
	"github.com/vulnetix/belai/internal/proserules"
	"github.com/vulnetix/belai/internal/rolemanager"
)

// TestClassifierSendsProseRules pins that every role prompt leaves with the
// prose rules after its own text, once.
func TestClassifierSendsProseRules(t *testing.T) {
	var system string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, m := range body.Messages {
			if m.Role == "system" {
				system = m.Content
			}
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"SAFE"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()

	c := NewClassifier(Config{Provider: "openai", BaseURL: srv.URL, APIKey: "sk", Model: "gpt-5"}, srv.Client())
	if _, err := c.Classify(context.Background(), rolemanager.BuildClassifierPayload("u")); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if !strings.HasPrefix(system, "You are a security classifier") {
		t.Fatalf("system prompt lost its own text first: %.60q", system)
	}
	if n := strings.Count(system, proserules.Rules); n != 1 {
		t.Fatalf("system prompt carries the prose rules %d times, want 1", n)
	}
}

// TestSealSystemCarriesProseRules pins the rules into the main agent prompt.
func TestSealSystemCarriesProseRules(t *testing.T) {
	sealed, err := SealSystem(Config{Provider: "openai", Model: "gpt-5"}, sealPool(t), prompt.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(sealed, proserules.Rules); n != 1 {
		t.Fatalf("sealed system prompt carries the prose rules %d times, want 1", n)
	}
}
