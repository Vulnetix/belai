package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
)

// serversFor stands up a chat server (answering SAFE, counting calls) and a
// systemone server (answering every question false, counting calls).
func serversFor(t *testing.T) (chat, so *httptest.Server, chatN, soN *atomic.Int32) {
	t.Helper()
	chatN, soN = &atomic.Int32{}, &atomic.Int32{}
	chat = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound) // the session's nonce seeding
			return
		}
		chatN.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "x", "object": "chat.completion", "choices": []any{
			map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": "SAFE"}, "finish_reason": "stop"}}})
	}))
	so = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		soN.Add(1)
		var req struct {
			Questions map[string]any `json:"questions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		ans := map[string]any{}
		for k := range req.Questions {
			ans[k] = map[string]any{"type": "noul", "noul": 0.01}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"answers": ans})
	}))
	t.Cleanup(chat.Close)
	t.Cleanup(so.Close)
	return
}

// Through the agent's own tool-result path (Session.promoteResult), a Read of
// a doc reaches the SystemOne decision backend when one is configured and the
// chat classifier when not, and a Read of comment-free code reaches neither.
func TestPromoteResultRoutesReadsByContentClass(t *testing.T) {
	chat, so, chatN, soN := serversFor(t)
	cases := []struct {
		name, file, body string
		systemOne        bool
		wantChat, wantSO int32
	}{
		{"doc with SystemOne", "README.md", "# Title\n", true, 0, 1},
		{"doc without SystemOne", "README.md", "# Title\n", false, 1, 0},
		{"code with a comment, SystemOne", "a.go", "package a\n// note\n", true, 0, 1},
		{"code without comments", "a.go", "package a\nfunc f() {}\n", true, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			chatN.Store(0)
			soN.Store(0)
			cfg := run.Config{Provider: "openai", BaseURL: chat.URL, APIKey: "k", Model: "m"}
			if c.systemOne {
				cfg.Classifier = run.ClassifierConfig{Provider: "openai", Model: "m", BaseURL: chat.URL, APIKey: "k",
					Decisions: run.DecisionsConfig{Backend: decisions.BackendSystemOne, Provider: "so", Model: "so", BaseURL: so.URL}}
			}
			s, err := NewSession(Options{Cfg: cfg, Client: http.DefaultClient, Posture: posture.Defaults()})
			if err != nil {
				t.Fatal(err)
			}
			res := readFile(t, c.file, c.body)
			out := s.promoteResult(t.Context(), rolemanager.ToolCall{ID: "1", Name: "Read"}, res, func(Event) {})
			if strings.Contains(out, "tool result withheld") {
				t.Fatalf("withheld: %s", out)
			}
			if chatN.Load() != c.wantChat || soN.Load() != c.wantSO {
				t.Fatalf("chat %d systemone %d, want %d %d", chatN.Load(), soN.Load(), c.wantChat, c.wantSO)
			}
		})
	}
}
