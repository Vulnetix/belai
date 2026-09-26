package tui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/sessionsync"
)

// ackRecorder is a minimal /v1/belai that records prompt acks and accepts
// everything else.
type ackRecorder struct {
	mu   sync.Mutex
	acks map[string]map[string]string
}

func (r *ackRecorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if strings.Contains(req.URL.Path, "/prompts/") && strings.HasSuffix(req.URL.Path, "/ack") {
		var in map[string]string
		_ = json.NewDecoder(req.Body).Decode(&in)
		id := strings.Split(strings.TrimPrefix(req.URL.Path, "/api/site/v1/belai/prompts/"), "/")[0]
		r.acks[id] = in
	}
	if strings.HasSuffix(req.URL.Path, "/inbox") {
		_, _ = w.Write([]byte(`{"prompts":[]}`))
		return
	}
	_, _ = w.Write([]byte(`{"lastSeq":-1}`))
}

func (r *ackRecorder) ack(t *testing.T, id string) map[string]string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		got, ok := r.acks[id]
		r.mu.Unlock()
		if ok {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no ack for %s", id)
	return nil
}

func newSyncApp(t *testing.T) (*App, *ackRecorder) {
	t.Helper()
	a := newPersistApp(t)
	a.mode = "chat"
	rec := &ackRecorder{acks: map[string]map[string]string{}}
	srv := httptest.NewServer(rec)
	t.Cleanup(srv.Close)
	c, err := sessionsync.NewClient(srv.URL+"/api/site/v1/belai", func() (string, error) { return "ApiKey o:k", nil }, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	a.syncer = sessionsync.New(sessionsync.Options{Client: c, HostID: "11111111-1111-4111-8111-111111111111", RemotePrompts: true})
	a.syncer.Start(context.Background())
	t.Cleanup(func() { a.syncer.Close(time.Second) })
	return a, rec
}

// A web prompt that arrives mid-turn is queued, acked as queued, and runs
// once the host is idle — written to the JSONL as a user line that carries
// its request id, which is how the website matches it.
func TestRemotePromptQueuesThenRunsAsUserLine(t *testing.T) {
	a, rec := newSyncApp(t)
	a.cancel = func() {} // a turn is running
	p := sessionsync.RemotePrompt{ID: "p1", SessionID: a.sessionID, Content: "run the tests"}

	a.handleRemotePrompt(p)
	if got := rec.ack(t, "p1"); got["status"] != sessionsync.AckQueued {
		t.Fatalf("ack = %v, want queued", got)
	}
	if len(a.remoteQueue) != 1 {
		t.Fatalf("queue = %d, want 1", len(a.remoteQueue))
	}
	if a.drainRemoteQueue() != nil || len(a.remoteQueue) != 1 {
		t.Fatal("drained while the turn was still running")
	}

	a.cancel = nil
	_ = a.drainRemoteQueue()
	if len(a.remoteQueue) != 0 {
		t.Fatal("queue not drained once idle")
	}
	entries := persistedEntries(t, a)
	if len(entries) == 0 {
		t.Fatal("no user line written")
	}
	u := entries[0]
	if u.Type != "user" || u.Content != "run the tests" || u.Meta["remote_prompt_id"] != "p1" || u.Meta["source"] != "web" {
		t.Fatalf("user entry = %+v", u)
	}
	rec.mu.Lock()
	delete(rec.acks, "p1")
	rec.mu.Unlock()
	if got := rec.ack(t, "p1"); got["status"] != sessionsync.AckAccepted || got["entryId"] != u.ID {
		t.Fatalf("ack = %v, want accepted with entry %s", got, u.ID)
	}
	// The TUI shows it as a web prompt.
	last := a.messages[len(a.messages)-1]
	for i := len(a.messages) - 1; i >= 0; i-- {
		if a.messages[i].Role == "user" {
			last = a.messages[i]
			break
		}
	}
	if last.RemoteID != "p1" {
		t.Fatalf("transcript user message RemoteID = %q", last.RemoteID)
	}
}

// The website can never run a slash command, a shell command or terminal
// escapes: a leading "/" or "!" is plain prompt text and control runes go.
func TestRemotePromptIsNeverALocalCommand(t *testing.T) {
	a, rec := newSyncApp(t)
	p := sessionsync.RemotePrompt{ID: "p2", SessionID: a.sessionID, Content: "!rm -rf /\x1b[2J"}
	a.handleRemotePrompt(p)
	rec.ack(t, "p2")
	entries := persistedEntries(t, a)
	if len(entries) == 0 || entries[0].Content != "!rm -rf /[2J" {
		t.Fatalf("entries = %+v", entries)
	}
	for _, m := range a.messages {
		if m.Role == "shell" {
			t.Fatal("a web prompt ran as a shell command")
		}
	}
}

func TestRemotePromptForAnotherSessionIsRefused(t *testing.T) {
	a, rec := newSyncApp(t)
	a.handleRemotePrompt(sessionsync.RemotePrompt{ID: "p3", SessionID: "other", Content: "hi"})
	if got := rec.ack(t, "p3"); got["status"] != sessionsync.AckRefused {
		t.Fatalf("ack = %v, want refused", got)
	}
	if a.hasUserMessage() {
		t.Fatal("a refused prompt reached the transcript")
	}
}

// Agent mode with no agent chosen waits on the host's picker, which the
// website cannot answer.
func TestRemotePromptRefusedWithoutAgentCarrier(t *testing.T) {
	a, rec := newSyncApp(t)
	a.mode, a.namedAgent = "agent", ""
	a.handleRemotePrompt(sessionsync.RemotePrompt{ID: "p4", SessionID: a.sessionID, Content: "hi"})
	if got := rec.ack(t, "p4"); got["status"] != sessionsync.AckRefused || !strings.Contains(got["reason"], "agent") {
		t.Fatalf("ack = %v", got)
	}
}

// The composer is the host user's: a web prompt leaves their draft alone.
func TestRemotePromptLeavesComposerDraft(t *testing.T) {
	a, rec := newSyncApp(t)
	a.editor.SetValue("half-typed")
	a.handleRemotePrompt(sessionsync.RemotePrompt{ID: "p5", SessionID: a.sessionID, Content: "hi"})
	rec.ack(t, "p5")
	if a.editor.Value() != "half-typed" {
		t.Fatalf("draft = %q", a.editor.Value())
	}
}
