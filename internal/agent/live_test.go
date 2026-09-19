package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/tools"
)

// multiToolServer emits nToolCalls of the given tool before answering "done".
// It counts security-classifier calls so a test can assert a mid-turn posture
// flip changes whether a tool result pays the classifier round trip.
type multiToolServer struct {
	mu            sync.Mutex
	securityCalls int
}

func newMultiToolServer(toolName, toolArgs string, nToolCalls int) (*httptest.Server, *multiToolServer) {
	probe := &multiToolServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		var system string
		toolResults := 0
		for _, m := range req.Messages {
			switch m.Role {
			case "system":
				system = m.Content
			case "tool":
				toolResults++
			}
		}

		switch {
		case contains(system, "security classifier"):
			probe.mu.Lock()
			probe.securityCalls++
			probe.mu.Unlock()
			writeChatJSON(w, "SAFE")
		case contains(system, "operating-mode classifier"):
			writeChatJSON(w, "AGENT")
		default:
			msg := map[string]any{"role": "assistant", "content": ""}
			if toolResults < nToolCalls {
				msg["tool_calls"] = []any{map[string]any{
					"id":       fmt.Sprintf("call_%d", toolResults),
					"type":     "function",
					"function": map[string]any{"name": toolName, "arguments": toolArgs},
				}}
			} else {
				msg["content"] = "done"
			}
			b, _ := json.Marshal(map[string]any{
				"id":      "x",
				"object":  "chat.completion",
				"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": "stop"}},
			})
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(b)
		}
	}))
	return srv, probe
}

// TestLiveAskGateTakesEffectMidTurn pins the ask-off direction: a session
// built with ask enabled prompts on the first mutating call; flipping the
// shared Live to ask-off between calls makes the second call run without a
// prompt, inside the same turn.
func TestLiveAskGateTakesEffectMidTurn(t *testing.T) {
	root := t.TempDir()
	srv, _ := newMultiToolServer("Write", writeArgs(), 2)
	defer srv.Close()

	live := posture.NewLive(posture.Defaults(), false)
	sess, err := NewSession(Options{
		Cfg:      run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "test"},
		Client:   srv.Client(),
		Registry: tools.NewRegistry(&tools.Write{Root: root}),
		Live:     live,
		Workdir:  root,
		AllowAsk: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	askCount := 0
	res, err := sess.run(context.Background(), nil, TurnInput{Prompt: "write twice"}, false, func(e Event) {
		if e.Kind == EventPermissionAskKind {
			askCount++
			e.AskReply <- PermissionAskReply{Allow: true}
			// Between calls: the operator turns ask off. The next gate check
			// must see it without waiting for a new session.
			live.Set(posture.Defaults(), true)
		}
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Reply != "done" {
		t.Fatalf("reply = %q", res.Reply)
	}
	if askCount != 1 {
		t.Fatalf("ask events = %d, want exactly 1 (second call must skip the prompt)", askCount)
	}
}

// TestLiveAskGateTakesEffectMidTurnReverse pins the ask-on direction: a
// session built with ask off runs the first mutating call without a prompt;
// flipping the shared Live to ask-on between calls makes the second call
// prompt, inside the same turn.
func TestLiveAskGateTakesEffectMidTurnReverse(t *testing.T) {
	root := t.TempDir()
	srv, _ := newMultiToolServer("Write", writeArgs(), 2)
	defer srv.Close()

	live := posture.NewLive(posture.Defaults(), true)
	sess, err := NewSession(Options{
		Cfg:      run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "test"},
		Client:   srv.Client(),
		Registry: tools.NewRegistry(&tools.Write{Root: root}),
		Live:     live,
		Workdir:  root,
		AllowAsk: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	askCount := 0
	toolResults := 0
	res, err := sess.run(context.Background(), nil, TurnInput{Prompt: "write twice"}, false, func(e Event) {
		if e.Kind == EventToolResultKind {
			toolResults++
			if toolResults == 1 {
				// The first call completed without a prompt; turn ask back on.
				live.Set(posture.Defaults(), false)
			}
		}
		if e.Kind == EventPermissionAskKind {
			askCount++
			e.AskReply <- PermissionAskReply{Allow: true}
		}
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Reply != "done" {
		t.Fatalf("reply = %q", res.Reply)
	}
	if askCount != 1 {
		t.Fatalf("ask events = %d, want exactly 1 (second call must prompt)", askCount)
	}
}

// TestLiveToolResultGateTakesEffectMidTurn pins the classifier gate: with
// enforce on, the first Read result is classified; flipping the shared Live to
// Ignore between results makes the second Read result skip the round trip,
// inside the same turn.
func TestLiveToolResultGateTakesEffectMidTurn(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	srv, probe := newMultiToolServer("Read", `{"path":"f.txt"}`, 2)
	defer srv.Close()

	live := posture.NewLive(posture.Defaults(), false)
	sess, err := NewSession(Options{
		Cfg:      run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "test"},
		Client:   srv.Client(),
		Registry: tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024}),
		Live:     live,
		Workdir:  root,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	toolResults := 0
	res, err := sess.run(context.Background(), nil, TurnInput{Prompt: "read twice"}, false, func(e Event) {
		if e.Kind == EventToolResultKind {
			toolResults++
			if toolResults == 1 {
				live.Set(posture.AllIgnore(), false)
			}
		}
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Reply != "done" {
		t.Fatalf("reply = %q", res.Reply)
	}

	probe.mu.Lock()
	calls := probe.securityCalls
	probe.mu.Unlock()
	if calls != 2 {
		t.Fatalf("security classifier calls = %d, want 2 (admission plus the first result only)", calls)
	}
}
