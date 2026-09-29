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

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/jsonrpc"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/testpass"
	"github.com/vulnetix/belai/internal/testrun"
	"github.com/vulnetix/belai/internal/tools"
	"github.com/vulnetix/belai/internal/turnlog"
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

// Every method in Methods is handled, and nothing else is.
func TestMethodsParity(t *testing.T) {
	s := &Server{sessions: map[string]*acpSession{}, ready: make(chan struct{})}
	close(s.ready)
	for _, m := range Methods {
		_, err := s.handle(context.Background(), m, json.RawMessage(`{}`))
		if rpc, ok := err.(*jsonrpc.Error); ok && rpc.Code == jsonrpc.CodeMethodNotFound {
			t.Errorf("%s is listed but not handled", m)
		}
	}
	if _, err := s.handle(context.Background(), "session/load", nil); err == nil {
		t.Fatal("unlisted method handled")
	}
}

// A second prompt while a turn runs is refused; an empty prompt is refused.
func TestPromptGuards(t *testing.T) {
	s := &Server{sessions: map[string]*acpSession{}, ready: make(chan struct{})}
	close(s.ready)
	s.sessions["busy"] = &acpSession{id: "busy", cancel: func() {}, always: map[string]bool{}}
	s.sessions["idle"] = &acpSession{id: "idle", always: map[string]bool{}}
	_, err := s.handle(context.Background(), "session/prompt", json.RawMessage(`{"sessionId":"busy","prompt":[{"type":"text","text":"hi"}]}`))
	if rpc, ok := err.(*jsonrpc.Error); !ok || rpc.Code != jsonrpc.CodeInvalidRequest {
		t.Fatalf("busy session: %v", err)
	}
	_, err = s.handle(context.Background(), "session/prompt", json.RawMessage(`{"sessionId":"idle","prompt":[{"type":"text","text":"  "}]}`))
	if rpc, ok := err.(*jsonrpc.Error); !ok || rpc.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("empty prompt: %v", err)
	}
}

func TestToolKindsAreACPKinds(t *testing.T) {
	valid := map[string]bool{"read": true, "edit": true, "delete": true, "move": true, "search": true, "execute": true, "think": true, "fetch": true, "switch_mode": true, "other": true}
	for _, n := range []string{"Read", "Write", "Edit", "Grep", "Bash", "WebFetch", "update_plan", "mcp__x__y", "Skill", "SkillDraft"} {
		if !valid[toolKind(n)] {
			t.Errorf("toolKind(%s) = %q, not an ACP ToolKind", n, toolKind(n))
		}
	}
}

// A session opened with a transcript keeps the turn and every role-manager
// decision made while it ran, and the transcript goes to disk, never to the
// protocol stream.
func TestSessionTranscriptRecordsTheTurnAndItsDecisions(t *testing.T) {
	srv := mockProvider()
	defer srv.Close()
	t.Setenv("BELAI_HOME", t.TempDir())
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
	proto := &lockedBuffer{}
	sr, cw := io.Pipe()
	cr, sw := io.Pipe()
	tee := io.MultiWriter(sw, proto)
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() {
		defer close(served)
		ServeWith(ctx, sr, tee, build, Options{Transcript: func(cwd, id string) *turnlog.Log {
			l, err := turnlog.Open(cwd, id, session.Meta{Cwd: cwd, Mode: "agent"})
			if err != nil {
				t.Errorf("Open: %v", err)
			}
			return l
		}})
	}()
	ed := &editor{answer: "allow_always"}
	c := jsonrpc.NewConn(cr, cw, ed.handle)
	rctx, rcancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rcancel()
	var ns struct {
		SessionID string `json:"sessionId"`
	}
	if err := c.Call(rctx, "session/new", map[string]any{"cwd": root}, &ns); err != nil {
		t.Fatal(err)
	}
	var pr struct {
		StopReason string `json:"stopReason"`
	}
	if err := c.Call(rctx, "session/prompt", map[string]any{"sessionId": ns.SessionID, "prompt": []any{map[string]any{"type": "text", "text": "write x.txt"}}}, &pr); err != nil {
		t.Fatal(err)
	}
	cancel()
	<-served

	store, err := session.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	key, err := session.KeyFor(root)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := store.ReadFrom(key, ns.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]int{}
	for _, e := range entries {
		types[e.Type]++
	}
	if types["session_meta"] != 1 || types["user"] != 1 || types["assistant"] == 0 || types["tool"] != 1 {
		t.Fatalf("transcript entry types = %v", types)
	}
	if types[rolemanager.RecordType] == 0 {
		t.Fatalf("no role-manager decision was recorded: %v", types)
	}
	for _, line := range strings.Split(strings.TrimSpace(proto.String()), "\n") {
		if line != "" && !strings.HasPrefix(line, "{") {
			t.Fatalf("stdout carries a non-protocol line: %q", line)
		}
	}
}

// lockedBuffer is a bytes.Buffer safe to read while the server still writes.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// Without a transcript opener the connection keeps none.
func TestServeWithoutATranscriptKeepsNone(t *testing.T) {
	s := &Server{sessions: map[string]*acpSession{}, ready: make(chan struct{})}
	if s.opts.Transcript != nil {
		t.Fatal("default options open a transcript")
	}
}

// postEndServer is a Server wired to a fake editor over pipes, with the given
// post-end hook, for driving postEnd directly.
func postEndServer(t *testing.T, hook func(context.Context, string, *agent.Session, testpass.Fixer, func(string)) (testpass.Outcome, bool)) (*Server, *editor) {
	t.Helper()
	sr, cw := io.Pipe()
	cr, sw := io.Pipe()
	ed := &editor{}
	s := &Server{sessions: map[string]*acpSession{}, ready: make(chan struct{}), opts: Options{PostEnd: hook}}
	s.conn = jsonrpc.NewConn(sr, sw, s.handle)
	close(s.ready)
	client := jsonrpc.NewConn(cr, cw, ed.handle)
	t.Cleanup(func() { client.Close(); s.conn.Close() })
	return s, ed
}

func editorText(ed *editor) string {
	ed.mu.Lock()
	defer ed.mu.Unlock()
	var b strings.Builder
	for _, u := range ed.updates {
		if c, ok := u["content"].(map[string]any); ok {
			b.WriteString(c["text"].(string))
		}
	}
	return b.String()
}

func TestPostEndStreamsTheOutcomeToTheEditor(t *testing.T) {
	var gotCwd string
	s, ed := postEndServer(t, func(_ context.Context, cwd string, _ *agent.Session, fix testpass.Fixer, notify func(string)) (testpass.Outcome, bool) {
		gotCwd = cwd
		if fix == nil {
			t.Error("the editor fix loop was not supplied")
		}
		notify("running tests: go")
		return testpass.Outcome{
			Trigger: testpass.TriggerGoal, Passed: true,
			Results: []testrun.Result{{Suite: "go", Status: testrun.Passed}},
			Report:  "Tests passed: go pass in 1s.",
		}, true
	})
	s.postEnd(context.Background(), &acpSession{id: "s1", cwd: "/work", log: turnlog.New(nil)})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(editorText(ed), "Tests passed") {
		time.Sleep(10 * time.Millisecond)
	}
	text := editorText(ed)
	if gotCwd != "/work" {
		t.Fatalf("cwd = %q", gotCwd)
	}
	for _, want := range []string{"running tests: go", "tests pass (go pass)", "Tests passed: go pass in 1s."} {
		if !strings.Contains(text, want) {
			t.Errorf("editor text missing %q: %q", want, text)
		}
	}
}

func TestPostEndSettingsOffSendsNothing(t *testing.T) {
	s, ed := postEndServer(t, func(context.Context, string, *agent.Session, testpass.Fixer, func(string)) (testpass.Outcome, bool) {
		return testpass.Outcome{}, false
	})
	s.postEnd(context.Background(), &acpSession{id: "s1", cwd: "/work", log: turnlog.New(nil)})
	time.Sleep(100 * time.Millisecond)
	if got := editorText(ed); got != "" {
		t.Fatalf("editor received %q with the pass off", got)
	}
}

func TestPostEndWithoutAHookIsANoOp(t *testing.T) {
	s, ed := postEndServer(t, nil)
	s.postEnd(context.Background(), &acpSession{id: "s1", cwd: "/work", log: turnlog.New(nil)})
	time.Sleep(100 * time.Millisecond)
	if got := editorText(ed); got != "" {
		t.Fatalf("editor received %q with no hook", got)
	}
}
