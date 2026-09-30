package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

// echoProvider answers the classifiers and replies "ok" to the agent, and
// records which of the words in marks appear in the user messages of each
// agent request, so a test can see what history the model was given.
type echoProvider struct {
	*httptest.Server
	mu    sync.Mutex
	marks []string
	seen  []string // per agent request: the marks present, joined
}

func newEchoProvider(marks ...string) *echoProvider {
	p := &echoProvider{marks: marks}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Stream   bool `json:"stream"`
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		system, users := "", ""
		for _, m := range req.Messages {
			s, _ := m.Content.(string)
			if m.Role == "system" {
				system = s
			}
			if m.Role == "user" {
				users += s + "\n"
			}
		}
		content := "ok"
		switch {
		case bytes.Contains([]byte(system), []byte("security classifier")):
			content = "SAFE"
		case bytes.Contains([]byte(system), []byte("operating-mode classifier")):
			content = "AGENT"
		default:
			var have []string
			for _, m := range p.marks {
				if strings.Contains(users, m) {
					have = append(have, m)
				}
			}
			p.mu.Lock()
			p.seen = append(p.seen, strings.Join(have, "+"))
			p.mu.Unlock()
		}
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": content}}}})
			w.Write([]byte("data: " + string(chunk) + "\n\ndata: [DONE]\n\n"))
			return
		}
		b, _ := json.Marshal(map[string]any{"id": "x", "object": "chat.completion",
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}}})
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	}))
	return p
}

func (p *echoProvider) last() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.seen) == 0 {
		return "<none>"
	}
	return p.seen[len(p.seen)-1]
}

// treeRig is a server with a transcript, an editor and one session.
type treeRig struct {
	t    *testing.T
	c    *jsonrpc.Conn
	ed   *editor
	id   string
	log  *turnlog.Log
	prov *echoProvider
}

func newTreeRig(t *testing.T) *treeRig {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1") // never reach a real account
	prov := newEchoProvider("first", "second", "third")
	t.Cleanup(prov.Close)
	root := t.TempDir()
	build := func(ctx context.Context, cwd, id string) (*agent.Session, error) {
		return agent.NewSession(agent.Options{
			Cfg:       run.Config{Provider: "openai", BaseURL: prov.URL, APIKey: "k", Model: "test"},
			Client:    prov.Client(),
			Registry:  tools.NewRegistry(&tools.Write{Root: cwd, MaxBytes: 1024}),
			Posture:   posture.Defaults(),
			Workdir:   cwd,
			SessionID: id,
			AllowAsk:  true,
		})
	}
	rig := &treeRig{t: t, ed: &editor{answer: "allow_always"}, prov: prov}
	sr, cw := io.Pipe()
	cr, sw := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go ServeWith(ctx, sr, sw, build, Options{Transcript: func(cwd, id string) *turnlog.Log {
		l, err := turnlog.Open(cwd, id, session.Meta{Cwd: cwd, Mode: "agent"})
		if err != nil {
			t.Errorf("Open: %v", err)
		}
		rig.log = l
		return l
	}})
	rig.c = jsonrpc.NewConn(cr, cw, rig.ed.handle)
	var ns struct {
		SessionID string `json:"sessionId"`
	}
	if err := rig.c.Call(rig.ctx(), "session/new", map[string]any{"cwd": root}, &ns); err != nil {
		t.Fatal(err)
	}
	rig.id = ns.SessionID
	return rig
}

func (r *treeRig) ctx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	r.t.Cleanup(cancel)
	return ctx
}

// say sends a prompt and returns the agent text the editor got for it.
func (r *treeRig) say(text string) string {
	r.t.Helper()
	r.ed.mu.Lock()
	from := len(r.ed.updates)
	r.ed.mu.Unlock()
	var pr struct {
		StopReason string `json:"stopReason"`
	}
	if err := r.c.Call(r.ctx(), "session/prompt", map[string]any{"sessionId": r.id, "prompt": []any{map[string]any{"type": "text", "text": text}}}, &pr); err != nil {
		r.t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond) // notifications are dispatched concurrently
	r.ed.mu.Lock()
	defer r.ed.mu.Unlock()
	var b strings.Builder
	for _, u := range r.ed.updates[from:] {
		if u["sessionUpdate"] == "agent_message_chunk" {
			if c, ok := u["content"].(map[string]any); ok {
				b.WriteString(c["text"].(string))
			}
		}
	}
	return b.String()
}

func (r *treeRig) entries() []session.Entry {
	r.t.Helper()
	es, err := r.log.Writer().Entries()
	if err != nil {
		r.t.Fatal(err)
	}
	return es
}

// idOf finds a shown row by its label and returns its short id.
func (r *treeRig) idOf(label string) string {
	r.t.Helper()
	for _, row := range session.TreeRows(r.entries(), false) {
		if row.Label == label {
			return row.Entry.ID[:8]
		}
	}
	r.t.Fatalf("no row %q", label)
	return ""
}

func TestTreeAdvertisesItsCommand(t *testing.T) {
	rig := newTreeRig(t)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(strings.Join(rig.ed.kinds(), ","), "available_commands_update") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no command list sent: %v", rig.ed.kinds())
}

func TestTreeContinuesFromAnEarlierReply(t *testing.T) {
	rig := newTreeRig(t)
	rig.say("first")
	rig.say("second")
	if got := rig.prov.last(); got != "first+second" {
		t.Fatalf("second turn saw %q", got)
	}

	requests := len(rig.prov.seen)
	out := rig.say("/tree")
	if !strings.Contains(out, "user: first") || !strings.Contains(out, "user: second") || !strings.Contains(out, "●") {
		t.Fatalf("tree listing: %s", out)
	}
	if len(rig.prov.seen) != requests {
		t.Fatalf("/tree reached the model: %v", rig.prov.seen)
	}

	// Go back to the first reply and ask something else.
	reply := rig.idOf("assistant: ok")
	if out := rig.say("/tree " + reply); !strings.Contains(out, "Continuing from") {
		t.Fatalf("navigate: %s", out)
	}
	rig.say("third")
	if got := rig.prov.last(); got != "first+third" {
		t.Fatalf("after branching the model saw %q, want the first turn and the new one only", got)
	}

	entries := rig.entries()
	if !session.HasBranches(entries) {
		t.Fatal("no branch marker in the transcript")
	}
	forks := 0
	for _, row := range session.TreeRows(entries, false) {
		if row.Fork {
			forks++
		}
	}
	if forks != 1 {
		t.Fatalf("%d forks, want 1", forks)
	}
}

func TestTreeUserRowGoesBeforeThePrompt(t *testing.T) {
	rig := newTreeRig(t)
	rig.say("first")
	rig.say("second")
	out := rig.say("/tree " + rig.idOf("user: second"))
	if !strings.Contains(out, "Reword and send this prompt again") || !strings.Contains(out, "second") {
		t.Fatalf("no prompt offered back: %s", out)
	}
	rig.say("third")
	if got := rig.prov.last(); got != "first+third" {
		t.Fatalf("model saw %q", got)
	}
}

func TestTreeBeforeTheFirstPromptStartsOver(t *testing.T) {
	rig := newTreeRig(t)
	rig.say("first")
	rig.say("/tree " + rig.idOf("user: first"))
	rig.say("third")
	if got := rig.prov.last(); got != "third" {
		t.Fatalf("model saw %q, want only the new prompt", got)
	}
}

func TestTreeRefusesWhatItCannotRestore(t *testing.T) {
	rig := newTreeRig(t)
	rig.say("first")
	meta := rig.entries()[0].ID[:8] // a session_meta row is not a point to continue from
	for _, arg := range []string{"nosuchid", meta} {
		if out := rig.say("/tree " + arg); !strings.Contains(out, "/tree:") {
			t.Fatalf("%q was accepted: %s", arg, out)
		}
	}
	if session.HasBranches(rig.entries()) {
		t.Fatal("a refused navigation left a branch marker")
	}
	rig.say("second")
	if got := rig.prov.last(); got != "first+second" {
		t.Fatalf("history changed: %q", got)
	}
}
