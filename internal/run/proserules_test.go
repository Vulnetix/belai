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

// classifierSystem sends payload through a classifier and returns the system
// prompt the provider received.
func classifierSystem(t *testing.T, payload rolemanager.ClassifierPayload) string {
	t.Helper()
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
	if _, err := c.Classify(context.Background(), payload); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	return system
}

// TestClassifierSendsProseRules pins that a role prompt with a free-form reply
// leaves with the prose rules after its own text, once.
func TestClassifierSendsProseRules(t *testing.T) {
	system := classifierSystem(t, rolemanager.ClassifierPayload{System: "Summarize.", User: "u", UseCase: "compaction"})
	if !strings.HasPrefix(system, "Summarize.") {
		t.Fatalf("system prompt lost its own text first: %.60q", system)
	}
	if n := strings.Count(system, proserules.Rules); n != 1 {
		t.Fatalf("system prompt carries the prose rules %d times, want 1", n)
	}
}

// TestSentinelClassifierSendsNoProseRules pins that a prompt whose reply is one
// token is sent as written; with the rules appended every prompt was refused
// as "classifier reply was malformed".
func TestSentinelClassifierSendsNoProseRules(t *testing.T) {
	system := classifierSystem(t, rolemanager.BuildClassifierPayload("u"))
	if !strings.HasPrefix(system, "You are a security classifier") {
		t.Fatalf("system prompt lost its own text first: %.60q", system)
	}
	if strings.Contains(system, proserules.Rules) {
		t.Fatal("security classifier prompt carries the prose rules")
	}
	system = classifierSystem(t, rolemanager.ClassifierPayload{System: "Reply GOOD or BAD.", User: "u", UseCase: "goal_eval", AllowReasoningFallback: true})
	if strings.Contains(system, proserules.Rules) {
		t.Fatal("sentinel evaluator prompt carries the prose rules")
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
