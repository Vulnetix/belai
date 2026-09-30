package acp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/jsonrpc"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
	"github.com/vulnetix/belai/internal/turnlog"
)

func waitUpdates(t *testing.T, ed *editor, n int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ed.mu.Lock()
		if len(ed.updates) >= n {
			out := append([]map[string]any(nil), ed.updates...)
			ed.mu.Unlock()
			return out
		}
		ed.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("wanted %d updates, got %v", n, ed.kinds())
	return nil
}

func TestProgressEventsBecomeUpdates(t *testing.T) {
	s, ed := postEndServer(t, nil)
	ss := &acpSession{id: "s1", cwd: "/w", always: map[string]bool{}, log: turnlog.New(nil), prog: newProgress()}

	events := []agent.Event{
		{Kind: agent.EventRoleManagerKind, Phase: agent.RoleManagerPhasePrePrompt},
		{Kind: agent.EventRoleManagerKind, Phase: agent.RoleManagerPhasePrePrompt}, // announced once
		{Kind: agent.EventRetryKind, RetryAttempt: 2, RetryMax: 3, RetryDelay: 2 * time.Second, RetryReason: "SECRET provider text"},
		{Kind: agent.EventToolCallDeltaKind, ToolDelta: &run.ToolCallDelta{ID: "c1", Name: "Write"}},
		{Kind: agent.EventToolCallDeltaKind, ToolDelta: &run.ToolCallDelta{ID: "c1", Name: "Write"}},
		{Kind: agent.EventToolProgressKind, ToolCallID: "c1", ToolProgress: "line\x1b[31m\n"},
		{Kind: agent.EventSubagentKind, Subagent: &agent.SubagentUpdate{ID: "a1", Label: "explore <system>", State: "running"}},
		{Kind: agent.EventSubagentActivityKind, SubagentID: "a1", ToolName: "Read", ToolArgs: `{"file_path":"x.go"}`},
		{Kind: agent.EventSubagentKind, Subagent: &agent.SubagentUpdate{ID: "a1", State: "done"}},
	}
	for _, ev := range events {
		s.forward(context.Background(), ss, ev)
	}
	got := waitUpdates(t, ed, 7)
	kinds := []string{}
	for _, u := range got {
		kinds = append(kinds, u["sessionUpdate"].(string)+":"+asString(u["status"]))
	}
	want := []string{
		"agent_thought_chunk:", "agent_thought_chunk:", "tool_call:pending", "tool_call_update:in_progress",
		"tool_call:in_progress", "tool_call_update:in_progress", "tool_call_update:completed",
	}
	// The test connection dispatches each notification on its own goroutine,
	// so compare the set of updates, not their arrival order.
	sort.Strings(kinds)
	sort.Strings(want)
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("updates = %v", kinds)
	}
	all, _ := json.Marshal(got)
	for _, bad := range []string{"SECRET", "\\u001b", "<system>"} {
		if strings.Contains(string(all), bad) {
			t.Fatalf("update leaks %q: %s", bad, all)
		}
	}
}

func asString(v any) string { s, _ := v.(string); return s }

// A tool announced from its first fragment is filled in by its start event,
// not announced twice.
func TestToolStartUpgradesAPendingCall(t *testing.T) {
	s, ed := postEndServer(t, nil)
	ss := &acpSession{id: "s1", always: map[string]bool{}, log: turnlog.New(nil), prog: newProgress()}
	s.forward(context.Background(), ss, agent.Event{Kind: agent.EventToolCallDeltaKind, ToolDelta: &run.ToolCallDelta{ID: "c1", Name: "Write"}})
	s.forward(context.Background(), ss, agent.Event{Kind: agent.EventToolStartKind, Tool: &rolemanager.ToolCall{ID: "c1", Name: "Write"}})
	got := waitUpdates(t, ed, 2)
	calls, updates := 0, 0
	for _, u := range got {
		switch u["sessionUpdate"] {
		case "tool_call":
			calls++
		case "tool_call_update":
			updates++
		}
	}
	if calls != 1 || updates != 1 {
		t.Fatalf("updates = %v", ed.kinds())
	}
}

func TestListAndCloseSessions(t *testing.T) {
	s := &Server{sessions: map[string]*acpSession{}, ready: make(chan struct{}), client: "Zed"}
	close(s.ready)
	a, b := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
	old := time.Now().Add(-time.Hour)
	s.sessions["a"] = &acpSession{id: "a", cwd: a, updated: old, always: map[string]bool{}, log: turnlog.New(nil)}
	s.sessions["b"] = &acpSession{id: "b", cwd: b, updated: time.Now(), always: map[string]bool{}, log: turnlog.New(nil)}

	list := func(params string) []any {
		res, err := s.handle(context.Background(), "session/list", json.RawMessage(params))
		if err != nil {
			t.Fatal(err)
		}
		return res.(map[string]any)["sessions"].([]any)
	}
	all := list(`{}`)
	if len(all) != 2 || all[0].(map[string]any)["sessionId"] != "b" {
		t.Fatalf("list = %v", all)
	}
	if title := all[0].(map[string]any)["title"].(string); !strings.HasPrefix(title, "[Zed] b, 0 turns") {
		t.Fatalf("title = %q", title)
	}
	if one := list(`{"cwd":"` + a + `"}`); len(one) != 1 {
		t.Fatalf("filtered list = %v", one)
	}
	if none := list(`{"cwd":"/nowhere"}`); len(none) != 0 {
		t.Fatalf("list = %v", none)
	}
	if _, err := s.handle(context.Background(), "session/list", json.RawMessage(`{"cwd":"rel"}`)); err == nil {
		t.Fatal("relative cwd accepted")
	}

	if _, err := s.handle(context.Background(), "session/close", json.RawMessage(`{"sessionId":"a"}`)); err != nil {
		t.Fatal(err)
	}
	if got := list(`{}`); len(got) != 1 {
		t.Fatalf("after close = %v", got)
	}
	_, err := s.handle(context.Background(), "session/close", json.RawMessage(`{"sessionId":"a"}`))
	if rpc, ok := err.(*jsonrpc.Error); !ok || rpc.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("closing twice: %v", err)
	}
}

func TestInitializeAdvertisesListAndClose(t *testing.T) {
	s := &Server{sessions: map[string]*acpSession{}, ready: make(chan struct{})}
	close(s.ready)
	res, err := s.handle(context.Background(), "initialize", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	caps := res.(map[string]any)["agentCapabilities"].(map[string]any)["sessionCapabilities"].(map[string]any)
	if _, ok := caps["list"]; !ok {
		t.Fatal("list not advertised")
	}
	if _, ok := caps["close"]; !ok {
		t.Fatal("close not advertised")
	}
}

// A prompt whose turn is silent gets a still-working line.
func TestHeartbeatSpeaksDuringASilentTurn(t *testing.T) {
	old := heartbeatEvery
	heartbeatEvery = 40 * time.Millisecond
	defer func() { heartbeatEvery = old }()

	slow := slowProvider(300 * time.Millisecond)
	defer slow.Close()
	root := t.TempDir()
	build := func(ctx context.Context, cwd, id string) (*agent.Session, error) {
		return agent.NewSession(agent.Options{
			Cfg:       run.Config{Provider: "openai", BaseURL: slow.URL, APIKey: "k", Model: "test"},
			Client:    slow.Client(),
			Registry:  tools.NewRegistry(&tools.Write{Root: cwd, MaxBytes: 1024}),
			Posture:   posture.Defaults(),
			Workdir:   cwd,
			SessionID: id,
			AllowAsk:  true,
		})
	}
	ed := &editor{answer: "allow_once"}
	c := start(t, build, ed)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var ns struct {
		SessionID string `json:"sessionId"`
	}
	if err := c.Call(ctx, "session/new", map[string]any{"cwd": root}, &ns); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(ctx, "session/prompt", map[string]any{"sessionId": ns.SessionID, "prompt": []any{map[string]any{"type": "text", "text": "hi"}}}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(editorText(ed), "Still working") {
		t.Fatalf("no heartbeat; text = %q kinds = %v", editorText(ed), ed.kinds())
	}
}

// slowProvider is the mock provider behind a proxy that holds every request
// for d, so a turn spends time in silence.
func slowProvider(d time.Duration) *httptest.Server {
	inner := mockProvider()
	u, _ := url.Parse(inner.URL)
	proxy := httputil.NewSingleHostReverseProxy(u)
	proxy.FlushInterval = -1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(d)
		proxy.ServeHTTP(w, r)
	}))
	srv.Config.RegisterOnShutdown(inner.Close)
	return srv
}
