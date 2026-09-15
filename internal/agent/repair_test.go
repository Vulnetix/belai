package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/run"
)

func TestLengthStopReasonRefusesTools(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		// First call is nonce pool seeding; return empty success.
		if requests == 1 {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
			return
		}

		var req struct {
			Messages []struct {
				Role       string `json:"role"`
				Content    string `json:"content"`
				ToolCalls  []any  `json:"tool_calls,omitempty"`
				ToolCallID string `json:"tool_call_id,omitempty"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		// Security classifier.
		for _, m := range req.Messages {
			if m.Role == "system" && strings.Contains(m.Content, "security classifier") {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"SAFE"},"finish_reason":"stop"}]}`))
				return
			}
			if m.Role == "system" && strings.Contains(m.Content, "operating-mode") {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"AGENT"},"finish_reason":"stop"}]}`))
				return
			}
		}

		// If a tool result is already present, finish with plain text.
		for _, m := range req.Messages {
			if m.Role == "tool" {
				b, _ := json.Marshal(map[string]any{
					"choices": []any{map[string]any{
						"message":       map[string]any{"role": "assistant", "content": "done"},
						"finish_reason": "stop",
					}},
				})
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(b)
				return
			}
		}

		// Model turn: emit one tool call with stop_reason length.
		b, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{
					"role":          "assistant",
					"content":       "",
					"tool_calls":    []any{map[string]any{"id": "call_1", "type": "function", "function": map[string]any{"name": "Read", "arguments": "{}"}}},
					"finish_reason": "length",
				},
				"finish_reason": "length",
			}},
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	root := t.TempDir()
	sess := testSessionWithRead(t, root, posture.Defaults(), srv)

	var results []string
	res, err := sess.run(context.Background(), []run.Turn{}, TurnInput{Prompt: "read the file"}, false, func(e Event) {
		if e.Kind == EventToolResultKind {
			results = append(results, e.ToolResult)
		}
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(results) != 1 || !strings.Contains(results[0], "truncated") {
		t.Fatalf("expected truncated tool result, got %v", results)
	}
	_ = res
}
