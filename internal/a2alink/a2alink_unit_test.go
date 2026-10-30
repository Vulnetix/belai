package a2alink

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/a2a"
	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/repomemory"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sanitize"
)

// stubs for admit/received/sideTurn tests

type stubRecorder struct {
	mu      sync.Mutex
	entries []recorded
}

type recorded struct {
	typ, content string
	meta         map[string]any
}

func (r *stubRecorder) record(typ, content string, meta map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, recorded{typ: typ, content: content, meta: meta})
}

func (r *stubRecorder) find(typ string) (recorded, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.entries {
		if e.typ == typ {
			return e, true
		}
	}
	return recorded{}, false
}

func testLink(t *testing.T, opts ...func(*Options)) (*Link, *stubRecorder, func()) {
	dir := t.TempDir()
	const host = "22222222-2222-4222-8222-222222222222"
	off := false
	settings := config.Settings{Guardrails: &off}
	rec := &stubRecorder{}
	o := Options{
		Dir:       dir,
		HostID:    host,
		SessionID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		Profile:   "@test",
		Settings:  settings,
		Posture:   posture.AllIgnore(),
		Git:       gitJSON(),
		Record:    rec.record,
		PollEvery: 10 * time.Millisecond,
		Debounce:  time.Millisecond,
	}
	for _, fn := range opts {
		fn(&o)
	}
	l := Open(context.Background(), o)
	if l == nil {
		t.Fatal("Open returned nil")
	}
	return l, rec, func() { l.Close() }
}

const (
	inHost = "22222222-2222-4222-8222-222222222222"
	inSess = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
)

func messageEnvelope(text string) a2a.Envelope {
	return a2a.Envelope{
		Kind:      a2a.KindMessage,
		MessageID: "a2a-01msg000000000000000000000",
		ContextID: "a2a-01msg000000000000000000000",
		Agent:     a2a.AgentCard{ID: "33333333-3333-4333-8333-333333333333:dddddddd-dddd-4ddd-8ddd-dddddddddddd", HostID: "33333333-3333-4333-8333-333333333333", SessionID: "dddddddd-dddd-4ddd-8ddd-dddddddddddd", Profile: "peer"},
		Work:      a2a.Work{Repo: "o/r", Branch: "main"},
		To:        inHost + ":" + inSess,
		Parts:     []a2a.Part{{Text: text}},
	}
}

func replyEnvelope(ref, text string) a2a.Envelope {
	return a2a.Envelope{
		Kind:               a2a.KindReply,
		MessageID:          "a2a-01rpy000000000000000000000",
		ContextID:          "a2a-01msg000000000000000000000",
		Agent:              a2a.AgentCard{ID: "33333333-3333-4333-8333-333333333333:dddddddd-dddd-4ddd-8ddd-dddddddddddd", HostID: "33333333-3333-4333-8333-333333333333", SessionID: "dddddddd-dddd-4ddd-8ddd-dddddddddddd", Profile: "peer"},
		Work:               a2a.Work{Repo: "o/r", Branch: "main"},
		To:                 inHost + ":" + inSess,
		ReferenceMessageID: ref,
		Parts:              []a2a.Part{{Text: text}},
	}
}

func TestAdmitGuardrailsOff(t *testing.T) {
	l, _, cleanup := testLink(t)
	defer cleanup()

	raw := "hel\x00lo <script>world</script>"
	got, sent := l.admit(context.Background(), raw)
	want := sanitize.Text(raw)
	if want == raw {
		t.Fatal("sanitize.Text left the control character in place; the fixture no longer exercises the sanitize path")
	}
	if got != want {
		t.Fatalf("admit guardrails off = %q, want %q", got, want)
	}
	if sent != "" {
		t.Fatalf("admit guardrails off sentinel = %q, want empty", sent)
	}
}

func TestAdmitWithhold(t *testing.T) {
	on := true
	settings := config.Settings{Guardrails: &on}
	bad := posture.Policy{posture.ToolResultUnsafe: posture.Enforce}
	cfg, client := testClientAlways(t, rolemanager.SentinelPromptInjection)
	l, _, cleanup := testLink(t, func(o *Options) {
		o.Settings = settings
		o.Posture = bad
		o.Cfg = cfg
		o.Client = client
	})
	defer cleanup()

	got, sent := l.admit(context.Background(), "anything")
	if got != "" {
		t.Fatalf("admit withheld body = %q, want empty", got)
	}
	if sent != rolemanager.SentinelPromptInjection {
		t.Fatalf("admit withheld sentinel = %q, want %q", sent, rolemanager.SentinelPromptInjection)
	}
}

func TestAdmitPipelineError(t *testing.T) {
	on := true
	settings := config.Settings{Guardrails: &on}
	bad := posture.Policy{posture.ToolResultUnsafe: posture.Enforce}
	cfg, client := errorClient(t)
	l, _, cleanup := testLink(t, func(o *Options) {
		o.Settings = settings
		o.Posture = bad
		o.Cfg = cfg
		o.Client = client
	})
	defer cleanup()

	got, sent := l.admit(context.Background(), "anything")
	if got != "" {
		t.Fatalf("admit pipeline error body = %q, want empty", got)
	}
	if sent != "" {
		t.Fatalf("admit pipeline error sentinel = %q, want empty", sent)
	}
}

func TestAdmitProceed(t *testing.T) {
	on := true
	settings := config.Settings{Guardrails: &on}
	bad := posture.Policy{posture.ToolResultUnsafe: posture.Enforce}
	cfg, client := testClientAlways(t, rolemanager.SentinelSafe)
	l, _, cleanup := testLink(t, func(o *Options) {
		o.Settings = settings
		o.Posture = bad
		o.Cfg = cfg
		o.Client = client
	})
	defer cleanup()

	got, sent := l.admit(context.Background(), "safe text")
	if want := "safe text"; got != want {
		t.Fatalf("admit proceed = %q, want %q", got, want)
	}
	if sent != "" {
		t.Fatalf("admit proceed sentinel = %q, want empty", sent)
	}
}

func TestReceivedWithheld(t *testing.T) {
	l, rec, cleanup := testLink(t)
	defer cleanup()

	env := messageEnvelope("bad")
	l.received(context.Background(), a2a.Event{Env: env}, "", rolemanager.SentinelPromptInjection)

	if e, ok := rec.find(EntryMessageIn); !ok || !strings.Contains(e.content, "withheld") {
		t.Fatalf("expected withheld entry, got %v", rec.entries)
	}
	if e, ok := rec.find(EntryReplyOut); !ok || e.content != WithheldReply {
		t.Fatalf("expected harness reply, got %v", rec.entries)
	}
}

func TestReceivedMessageSideTurn(t *testing.T) {
	fake := &fakeSession{reply: "sure, I am done"}
	l, rec, cleanup := testLink(t)
	defer cleanup()
	l.Attach(fake)

	env := messageEnvelope("how is it going?")
	l.received(context.Background(), a2a.Event{Env: env}, "how is it going?", "")

	if got := fake.lastInput.Question; got != "how is it going?" {
		t.Fatalf("sideTurn question = %q, want %q", got, "how is it going?")
	}
	if e, ok := rec.find(EntryReplyOut); !ok || e.content != "sure, I am done" {
		t.Fatalf("expected reply out, got %v", rec.entries)
	}
}

func TestReceivedReply(t *testing.T) {
	fake := &fakeSession{}
	l, rec, cleanup := testLink(t)
	defer cleanup()
	l.Attach(fake)

	q := messageEnvelope("question text")
	env := replyEnvelope(q.MessageID, "answer")
	l.received(context.Background(), a2a.Event{Env: env, Question: &q}, "answer", "")

	if e, ok := rec.find(EntryReplyIn); !ok || e.content != "answer" {
		t.Fatalf("expected reply in, got %v", rec.entries)
	}
	if fake.lastNote.Directive == "" {
		t.Fatal("expected note delivered to session")
	}
}

func TestReceivedWithheldReplyNotDelivered(t *testing.T) {
	fake := &fakeSession{}
	l, rec, cleanup := testLink(t)
	defer cleanup()
	l.Attach(fake)

	env := replyEnvelope("a2a-01msg000000000000000000000", "")
	l.received(context.Background(), a2a.Event{Env: env}, "", rolemanager.SentinelPromptInjection)

	if _, ok := rec.find(EntryReplyIn); !ok {
		t.Fatal("expected reply_in entry")
	}
	if fake.peered {
		t.Fatal("withheld reply should not be delivered to the session")
	}
}

func TestSideTurnNilSession(t *testing.T) {
	l, rec, cleanup := testLink(t)
	defer cleanup()

	env := messageEnvelope("hello")
	l.sideTurn(context.Background(), env, "hello")

	if e, ok := rec.find(EntryReplyOut); !ok || e.content != FailedReply {
		t.Fatalf("expected failed reply, got %v", rec.entries)
	}
}

func TestSideTurnResultAndCalls(t *testing.T) {
	l, rec, cleanup := testLink(t)
	defer cleanup()
	fake := &fakeSession{reply: "ok", calls: []agent.SideCall{{Name: "RepoMemoryWrite", Args: `{"x":"y"}`, Result: "done"}}}
	l.Attach(fake)

	env := messageEnvelope("hello")
	l.sideTurn(context.Background(), env, "hello")

	if e, ok := rec.find(EntryReplyOut); !ok || e.content != "ok" {
		t.Fatalf("expected reply out, got %v", rec.entries)
	}
	if e, ok := rec.find(EntryReplyOut); ok {
		calls, _ := e.meta["calls"].([]agent.SideCall)
		if len(calls) != 1 || calls[0].Name != "RepoMemoryWrite" {
			t.Fatalf("expected calls in extra, got %v", e.meta)
		}
	}
}

func TestBuilderFunctions(t *testing.T) {
	if got := StateText(a2a.StateLocalOnly); !strings.Contains(got, "local only") {
		t.Fatalf("StateText local = %q", got)
	}
	if got := StateText(a2a.StateBlackout); !strings.Contains(got, "blackout") {
		t.Fatalf("StateText blackout = %q", got)
	}
	if got := StateText("relay"); !strings.Contains(got, "relaying") {
		t.Fatalf("StateText relay = %q", got)
	}

	w := a2a.Work{Repo: "o/r", Branch: "main", PR: 42, Ticket: "T-1"}
	if got := WorkLine(w); got != "o/r@main, PR #42, ticket T-1" {
		t.Fatalf("WorkLine = %q", got)
	}
	if got := WorkLine(a2a.Work{}); got != "" {
		t.Fatalf("WorkLine empty = %q", got)
	}

	if got := FilesLine([]string{}); got != "no files changed yet" {
		t.Fatalf("FilesLine empty = %q", got)
	}
	files := []string{"a.go", "b.go", "c.go", "d.go", "e.go", "f.go", "g.go", "h.go", "i.go", "j.go", "k.go", "l.go", "m.go"}
	if got := FilesLine(files); !strings.Contains(got, "(+1 more)") {
		t.Fatalf("FilesLine many = %q", got)
	}

	ack := a2a.Envelope{Decision: "ignore", Parts: []a2a.Part{{Text: "no overlap"}}}
	if got := AckLine(ack); got != "ignore: no overlap" {
		t.Fatalf("AckLine = %q", got)
	}
	ackNoText := a2a.Envelope{Decision: "wait"}
	if got := AckLine(ackNoText); got != "wait" {
		t.Fatalf("AckLine no text = %q", got)
	}

	env := a2a.Envelope{MessageID: "a2a-01msg000000000000000000001",
		Agent: a2a.AgentCard{ID: "33333333-3333-4333-8333-333333333333:dddddddd-dddd-4ddd-8ddd-dddddddddddd", SessionID: "dddddddd-dddd-4ddd-8ddd-dddddddddddd", Profile: "peer", Hostname: "host"},
		Work:  a2a.Work{Repo: "o/r", Branch: "main", Files: []string{"x.go"}},
	}
	bd := BroadcastDirective(env, []string{"x.go"})
	if !strings.Contains(bd, "A2A broadcast a2a-01msg000000000000000000001") || !strings.Contains(bd, "A2AAck{") {
		t.Fatalf("BroadcastDirective = %q", bd)
	}

	reply := a2a.Envelope{Agent: a2a.AgentCard{Profile: "peer", ID: "33333333-3333-4333-8333-333333333333:dddddddd-dddd-4ddd-8ddd-dddddddddddd"}, ReferenceMessageID: "a2a-01msg000000000000000000001"}
	if got := ReplyDirective(reply); !strings.Contains(got, "attached A2A reply") {
		t.Fatalf("ReplyDirective = %q", got)
	}

	q := a2a.Envelope{Parts: []a2a.Part{{Text: "question?"}}}
	note := ReplyNote(reply, &q, "answer")
	if !strings.Contains(note.Attachments[0].Body, "question?") || !strings.Contains(note.Attachments[0].Body, "answer") {
		t.Fatalf("ReplyNote = %+v", note)
	}
	noteNoQ := ReplyNote(reply, nil, "answer")
	if strings.Contains(noteNoQ.Attachments[0].Body, "question?") {
		t.Fatalf("ReplyNote no question = %+v", noteNoQ)
	}
}

func TestNilAttachAndStamp(t *testing.T) {
	var l *Link
	l.Attach(&fakeSession{})
	stamp := l.Stamp()
	if (stamp() != repomemory.Stamp{}) {
		t.Fatal("nil Stamp should return zero value")
	}
}

// A nil *agent.Session is a non-nil Session; Attach must still ignore it, so
// a side turn answers with the failed reply instead of panicking.
func TestAttachIgnoresATypedNilSession(t *testing.T) {
	l, rec, cleanup := testLink(t)
	defer cleanup()
	l.Attach((*agent.Session)(nil))
	if l.session() != nil {
		t.Fatal("a typed-nil session was attached")
	}
	l.sideTurn(context.Background(), messageEnvelope("hello"), "hello")
	if e, ok := rec.find(EntryReplyOut); !ok || e.content != FailedReply {
		t.Fatalf("expected failed reply, got %v", rec.entries)
	}
}

// helpers

type fakeSession struct {
	lastInput agent.SideReplyInput
	lastNote  agent.PeerNote
	reply     string
	calls     []agent.SideCall
	peered    bool
}

func (s *fakeSession) Peer(n agent.PeerNote) bool {
	s.lastNote = n
	s.peered = true
	return true
}

func (s *fakeSession) SideReply(_ context.Context, in agent.SideReplyInput) (agent.SideReplyResult, error) {
	s.lastInput = in
	return agent.SideReplyResult{Reply: s.reply, Calls: s.calls}, nil
}

func (s *fakeSession) SetFilesHook(func([]string)) {}

func testClientAlways(t *testing.T, s rolemanager.Sentinel) (run.Config, *http.Client) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":      "x",
			"object":  "chat.completion",
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": string(s)}, "finish_reason": "stop"}},
		})
	}))
	t.Cleanup(srv.Close)
	return classifierCfg(srv.URL), srv.Client()
}

func errorClient(t *testing.T) (run.Config, *http.Client) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return classifierCfg(srv.URL), srv.Client()
}

func classifierCfg(baseURL string) run.Config {
	return run.Config{Provider: "openai", BaseURL: baseURL, APIKey: "sk", Model: "gpt-5"}
}

// Both the fake and the real session satisfy the interface Attach takes.
var (
	_ Session = (*fakeSession)(nil)
	_ Session = (*agent.Session)(nil)
)
