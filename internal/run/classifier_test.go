package run

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/vulnetix/signet/internal/rolemanager"
)

// TestNewClassifierHonorsPayloadMaxTokens pins the per-call completion-budget
// contract: sentinel classifier calls use ClassifierMaxTokens (sized for
// reasoning models to finish their preamble and still emit the token), while a
// payload carrying an explicit MaxTokens (structured output like compaction or
// clarification) overrides it. A 16-token cap previously starved reasoning
// models mid-thought, leaving the reply content empty and the verdict
// MALFORMED.
func TestNewClassifierHonorsPayloadMaxTokens(t *testing.T) {
	var mu sync.Mutex
	var maxTokens []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MaxTokens int `json:"max_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		maxTokens = append(maxTokens, body.MaxTokens)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"SAFE"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()

	cfg := Config{Provider: "openai", BaseURL: srv.URL, APIKey: "sk", Model: "gpt-5"}
	c := NewClassifier(cfg, srv.Client())

	if _, err := c.Classify(context.Background(), rolemanager.ClassifierPayload{System: "s", User: "u"}); err != nil {
		t.Fatalf("Classify default: %v", err)
	}
	if _, err := c.Classify(context.Background(), rolemanager.ClassifierPayload{
		System: "s", User: "u", MaxTokens: rolemanager.ClassifierStructuredMaxTokens,
	}); err != nil {
		t.Fatalf("Classify override: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(maxTokens) != 2 {
		t.Fatalf("got %d requests, want 2", len(maxTokens))
	}
	if maxTokens[0] != ClassifierMaxTokens {
		t.Fatalf("default max_tokens = %d, want %d", maxTokens[0], ClassifierMaxTokens)
	}
	if maxTokens[1] != rolemanager.ClassifierStructuredMaxTokens {
		t.Fatalf("override max_tokens = %d, want %d", maxTokens[1], rolemanager.ClassifierStructuredMaxTokens)
	}
}
