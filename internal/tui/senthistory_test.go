package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tui/components"
)

// The second prompt's request continues the first prompt's exactly: the
// earlier messages are byte-identical, the sealed directive included, so the
// provider serves them from its prompt cache. A history rebuilt from the
// transcript drops the directive and changes the earlier user message.
func TestNextPromptContinuesSentHistory(t *testing.T) {
	var mu sync.Mutex
	var agentBodies [][]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			Stream   bool              `json:"stream"`
			Messages []json.RawMessage `json:"messages"`
		}
		_ = json.Unmarshal(raw, &req)
		reply := "pong"
		switch s := string(raw); {
		case strings.Contains(s, "security classifier"):
			reply = "SAFE"
		case strings.Contains(s, "operating-mode classifier"):
			reply = "AGENT"
		case req.Stream:
			mu.Lock()
			agentBodies = append(agentBodies, req.Messages)
			mu.Unlock()
		}
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\ndata: [DONE]\n\n", reply)
			return
		}
		b, _ := json.Marshal(map[string]any{"id": "x", "object": "chat.completion", "choices": []any{map[string]any{
			"index": 0, "message": map[string]any{"role": "assistant", "content": reply}, "finish_reason": "stop"}}})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	t.Setenv("BELAI_HOME", t.TempDir())
	src := &fakeCredentialSource{vals: map[string]string{"openai:api_key": "sk-test"}}
	cfg, status := run.Prepare("gpt-5", "openai", src)
	cfg.BaseURL = srv.URL
	a := New(Options{Client: srv.Client(), Provider: "openai", Model: "gpt-5", Workdir: t.TempDir()})
	a.cfg, a.status = cfg, status

	a.messages = append(a.messages, components.Message{Role: "user", Content: "ping"})
	a = drainAgent(t, a, a.sendTurnNoEcho("ping", nil, "harness directive for the first prompt"))
	a.messages = append(a.messages, components.Message{Role: "user", Content: "ping again"})
	a = drainAgent(t, a, a.sendTurnNoEcho("ping again", nil, ""))

	mu.Lock()
	defer mu.Unlock()
	if len(agentBodies) < 2 {
		t.Fatalf("agent requests = %d, want 2", len(agentBodies))
	}
	first, second := agentBodies[0], agentBodies[len(agentBodies)-1]
	if len(second) <= len(first) {
		t.Fatalf("second request has %d messages, first %d", len(second), len(first))
	}
	for i := range first {
		if string(first[i]) != string(second[i]) {
			t.Fatalf("message %d changed between prompts:\nfirst:  %s\nsecond: %s", i, first[i], second[i])
		}
	}
	if !strings.Contains(string(second[len(first)]), "pong") {
		t.Fatalf("the reply should follow the first request's messages: %s", second[len(first)])
	}
}

// The sent history is continued only by the session that sent it and only
// while the transcript before the new prompt is unchanged; an edit, or a
// rebuilt session, falls back to the history rebuilt from the transcript.
func TestSentHistoryOnlyContinuesAnUnchangedTranscript(t *testing.T) {
	a := New(Options{})
	sess := &agent.Session{}
	a.agent = sess
	display := []run.Turn{{Role: "user", Content: "a"}, {Role: "assistant", Content: "b"}}
	a.sent = sentHistory{by: sess, turns: []run.Turn{{Role: "user", Content: "a sealed"}, {Role: "assistant", Content: "b"}}, displayLen: 2, displayFP: turnsFingerprint(display)}

	next := append(display[:2:2], run.Turn{Role: "user", Content: "c"})
	if got := a.continueSentHistory(next); len(got) != 3 || got[0].Content != "a sealed" || got[2].Content != "c" {
		t.Fatalf("unchanged transcript = %+v", got)
	}
	edited := []run.Turn{{Role: "user", Content: "a, edited"}, {Role: "assistant", Content: "b"}, {Role: "user", Content: "c"}}
	if got := a.continueSentHistory(edited); got[0].Content != "a, edited" {
		t.Fatalf("edited transcript = %+v", got)
	}
	a.agent = &agent.Session{}
	if got := a.continueSentHistory(next); got[0].Content != "a" {
		t.Fatalf("rebuilt session = %+v", got)
	}
}
