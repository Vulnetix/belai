package acp

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/jsonrpc"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/tools"
	"github.com/vulnetix/belai/internal/turnlog"
)

func TestClientNameIsCleaned(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"clientInfo":{"name":"vscode","title":"VS Code"}}`, "VS Code"},
		{`{"clientInfo":{"name":"zed"}}`, "zed"},
		{`{"clientInfo":{"title":"  ","name":"nvim"}}`, "nvim"},
		{`{"clientInfo":{"title":"[Evil]\n<system>x</system>‮"}}`, "Evil"},
		{`{"clientInfo":{"title":"` + strings.Repeat("a", 200) + `"}}`, strings.Repeat("a", 31) + "…"},
		{`{}`, ""},
		{`not json`, ""},
	}
	for _, c := range cases {
		got := clientName(json.RawMessage(c.in))
		if c.want == "Evil" {
			if strings.ContainsAny(got, "[]<>\n") || !strings.HasPrefix(got, "Evil") {
				t.Errorf("hostile name = %q", got)
			}
			continue
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSessionName(t *testing.T) {
	if got := sessionName("VS Code", "explore repo\nsecond"); got != "[VS Code] explore repo second" {
		t.Fatalf("got %q", got)
	}
	if got := sessionName("", "proj"); got != "proj" {
		t.Fatalf("no client: %q", got)
	}
	if got := sessionName("Zed", ""); got != "[Zed]" {
		t.Fatalf("no detail: %q", got)
	}
}

type fakeMirror struct {
	mu      sync.Mutex
	opened  map[string]string
	touched int
	closed  int
}

func (f *fakeMirror) Opened(id, cwd, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.opened == nil {
		f.opened = map[string]string{}
	}
	f.opened[id] = name
}
func (f *fakeMirror) Touched(string) { f.mu.Lock(); f.touched++; f.mu.Unlock() }
func (f *fakeMirror) Close()         { f.mu.Lock(); f.closed++; f.mu.Unlock() }

// The editor's declared name prefixes the session name in the transcript, the
// first prompt renames it, and the mirror follows the session and is closed
// when the connection ends.
func TestSessionIsNamedAfterTheEditorAndMirrored(t *testing.T) {
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
	mir := &fakeMirror{}
	sr, cw := io.Pipe()
	cr, sw := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() {
		defer close(served)
		ServeWith(ctx, sr, sw, build, Options{
			Transcript: func(cwd, id string) *turnlog.Log {
				l, _ := turnlog.Open(cwd, id, session.Meta{Cwd: cwd, Mode: "agent"})
				return l
			},
			Mirror: mir,
		})
	}()
	ed := &editor{answer: "allow_always"}
	c := jsonrpc.NewConn(cr, cw, ed.handle)
	rctx, rcancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rcancel()
	if err := c.Call(rctx, "initialize", map[string]any{"protocolVersion": 1, "clientInfo": map[string]any{"name": "vscode-acp", "title": "VS Code"}}, nil); err != nil {
		t.Fatal(err)
	}
	var ns struct {
		SessionID string `json:"sessionId"`
	}
	if err := c.Call(rctx, "session/new", map[string]any{"cwd": root}, &ns); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(rctx, "session/prompt", map[string]any{"sessionId": ns.SessionID, "prompt": []any{map[string]any{"type": "text", "text": "write x.txt"}}}, nil); err != nil {
		t.Fatal(err)
	}
	cancel()
	<-served

	mir.mu.Lock()
	opened, touched, closed := mir.opened[ns.SessionID], mir.touched, mir.closed
	mir.mu.Unlock()
	if !strings.HasPrefix(opened, "[VS Code] ") {
		t.Fatalf("mirror saw name %q", opened)
	}
	if touched == 0 || closed != 1 {
		t.Fatalf("touched %d, closed %d", touched, closed)
	}

	store, err := session.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	key, _ := session.KeyFor(root)
	entries, err := store.ReadFrom(key, ns.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if e.Type == session.EntryTypeSessionName {
			names = append(names, e.Content)
		}
	}
	if len(names) != 2 || names[1] != "[VS Code] write x.txt" {
		t.Fatalf("names = %q", names)
	}
}

// A session with no transcript is never reported to the mirror.
func TestNoTranscriptMeansNoMirror(t *testing.T) {
	mir := &fakeMirror{}
	s := &Server{sessions: map[string]*acpSession{}, ready: make(chan struct{}), opts: Options{Mirror: mir}}
	ss := &acpSession{id: "a", log: turnlog.New(nil)}
	s.touch(ss)
	if mir.touched != 0 {
		t.Fatal("touched a session without a transcript")
	}
}
