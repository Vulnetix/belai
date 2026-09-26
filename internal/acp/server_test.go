package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/signet/internal/agent"
	"github.com/vulnetix/signet/internal/jsonrpc"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/tools"
)

// mockProvider answers the classifier SAFE, the mode classifier AGENT, and
// the agent with one Write call and then a final reply.
func mockProvider() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Stream   bool `json:"stream"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		system, hasTool := "", false
		for _, m := range req.Messages {
			if m.Role == "system" {
				system = m.Content
			}
			if m.Role == "tool" {
				hasTool = true
			}
		}
		msg := map[string]any{"role": "assistant", "content": ""}
		switch {
		case bytes.Contains([]byte(system), []byte("security classifier")):
			msg["content"] = "SAFE"
		case bytes.Contains([]byte(system), []byte("operating-mode classifier")):
			msg["content"] = "AGENT"
		case !hasTool:
			msg["tool_calls"] = []any{map[string]any{"id": "call_1", "type": "function",
				"function": map[string]any{"name": "Write", "arguments": `{"file_path":"x.txt","content":"hello"}`}}}
		default:
			msg["content"] = "done"
		}
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			delta := map[string]any{}
			if tc, ok := msg["tool_calls"].([]any); ok {
				call := tc[0].(map[string]any)
				call["index"] = 0
				delta["tool_calls"] = []any{call}
			} else {
				delta["content"] = msg["content"]
			}
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta}}})
			w.Write([]byte("data: " + string(chunk) + "\n\ndata: [DONE]\n\n"))
			return
		}
		b, _ := json.Marshal(map[string]any{"id": "x", "object": "chat.completion",
			"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": "stop"}}})
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	}))
}

type editor struct {
	mu      sync.Mutex
	updates []map[string]any
	asked   int
	answer  string
}

func (e *editor) handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "session/update":
		var p struct {
			Update map[string]any `json:"update"`
		}
		json.Unmarshal(params, &p)
		e.mu.Lock()
		e.updates = append(e.updates, p.Update)
		e.mu.Unlock()
		return nil, nil
	case "session/request_permission":
		e.mu.Lock()
		e.asked++
		ans := e.answer
		e.mu.Unlock()
		return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": ans}}, nil
	}
	return nil, jsonrpc.Errorf(jsonrpc.CodeMethodNotFound, "no")
}

func (e *editor) kinds() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, u := range e.updates {
		out = append(out, u["sessionUpdate"].(string))
	}
	return out
}

func start(t *testing.T, build Builder, ed *editor) *jsonrpc.Conn {
	t.Helper()
	sr, cw := io.Pipe()
	cr, sw := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go Serve(ctx, sr, sw, build)
	return jsonrpc.NewConn(cr, cw, ed.handle)
}

func TestPromptTurnWithPermission(t *testing.T) {
	srv := mockProvider()
	defer srv.Close()
	root := t.TempDir()
	build := func(ctx context.Context, cwd, id string) (*agent.Session, error) {
		return agent.NewSession(agent.Options{
			Cfg:       run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "test"},
			Client:    srv.Client(),
			Registry:  tools.NewRegistry(&tools.Write{Root: cwd, MaxBytes: 1024}),
			Posture:   posture.Defaults(),
			Workdir:   cwd,
			SessionID: id,
			AllowAsk:  true,
		})
	}
	ed := &editor{answer: "allow_always"}
	c := start(t, build, ed)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var init struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	if err := c.Call(ctx, "initialize", map[string]any{"protocolVersion": 1}, &init); err != nil || init.ProtocolVersion != 1 {
		t.Fatalf("initialize: %v %+v", err, init)
	}
	var ns struct {
		SessionID string `json:"sessionId"`
	}
	if err := c.Call(ctx, "session/new", map[string]any{"cwd": root, "mcpServers": []any{}}, &ns); err != nil || ns.SessionID == "" {
		t.Fatalf("session/new: %v", err)
	}
	var pr struct {
		StopReason string `json:"stopReason"`
	}
	if err := c.Call(ctx, "session/prompt", map[string]any{"sessionId": ns.SessionID, "prompt": []any{map[string]any{"type": "text", "text": "write x.txt"}}}, &pr); err != nil {
		t.Fatal(err)
	}
	if pr.StopReason != "end_turn" {
		t.Fatalf("stopReason = %q", pr.StopReason)
	}
	if b, err := os.ReadFile(filepath.Join(root, "x.txt")); err != nil || string(b) != "hello" {
		t.Fatalf("file not written after approval: %v; updates %v", err, ed.updates)
	}
	kinds := strings.Join(ed.kinds(), ",")
	for _, want := range []string{"tool_call", "tool_call_update"} {
		if !strings.Contains(kinds, want) {
			t.Errorf("updates %s lack %s", kinds, want)
		}
	}
	if ed.asked != 1 {
		t.Fatalf("asked %d times", ed.asked)
	}
	// allow_always: the second Write in the session does not ask again.
	os.Remove(filepath.Join(root, "x.txt"))
	if err := c.Call(ctx, "session/prompt", map[string]any{"sessionId": ns.SessionID, "prompt": []any{map[string]any{"type": "text", "text": "again"}}}, &pr); err != nil {
		t.Fatal(err)
	}
	if ed.asked != 1 {
		t.Fatalf("allow_always asked again: %d", ed.asked)
	}
}

func TestRejectedPermissionWritesNothing(t *testing.T) {
	srv := mockProvider()
	defer srv.Close()
	root := t.TempDir()
	build := func(ctx context.Context, cwd, id string) (*agent.Session, error) {
		return agent.NewSession(agent.Options{
			Cfg:      run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "test"},
			Client:   srv.Client(),
			Registry: tools.NewRegistry(&tools.Write{Root: cwd, MaxBytes: 1024}),
			Posture:  posture.Defaults(),
			Workdir:  cwd,
			AllowAsk: true,
		})
	}
	ed := &editor{answer: "reject_once"}
	c := start(t, build, ed)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var ns struct {
		SessionID string `json:"sessionId"`
	}
	c.Call(ctx, "session/new", map[string]any{"cwd": root, "mcpServers": []any{}}, &ns)
	var pr struct {
		StopReason string `json:"stopReason"`
	}
	if err := c.Call(ctx, "session/prompt", map[string]any{"sessionId": ns.SessionID, "prompt": []any{map[string]any{"type": "text", "text": "write"}}}, &pr); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "x.txt")); err == nil {
		t.Fatal("rejected write still wrote the file")
	}
}

func TestNewSessionErrors(t *testing.T) {
	ed := &editor{}
	c := start(t, func(ctx context.Context, cwd, id string) (*agent.Session, error) {
		return nil, io.ErrUnexpectedEOF
	}, ed)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var rpcErr *jsonrpc.Error
	if err := c.Call(ctx, "session/new", map[string]any{"cwd": "relative"}, nil); err == nil || !asRPC(err, &rpcErr) || rpcErr.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("relative cwd: %v", err)
	}
	if err := c.Call(ctx, "session/new", map[string]any{"cwd": "/tmp"}, nil); err == nil {
		t.Fatal("builder failure was not reported")
	}
	if err := c.Call(ctx, "session/prompt", map[string]any{"sessionId": "nope", "prompt": []any{}}, nil); err == nil {
		t.Fatal("unknown session accepted")
	}
}

func asRPC(err error, target **jsonrpc.Error) bool {
	e, ok := err.(*jsonrpc.Error)
	if ok {
		*target = e
	}
	return ok
}

func TestPromptText(t *testing.T) {
	got := promptText([]contentBlock{{Type: "text", Text: "fix"}, {Type: "resource_link", URI: "file:///a.go"}})
	if got != "fix (file: file:///a.go)" {
		t.Fatalf("got %q", got)
	}
}
