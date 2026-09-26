package sessionsync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testHost = "11111111-1111-4111-8111-111111111111"
	testSess = "22222222-2222-4222-8222-222222222222"
)

// fakeServer is an in-memory /v1/belai with the server's idempotency rule:
// a seq it already holds is ignored.
type fakeServer struct {
	mu       sync.Mutex
	auth     []string
	hosts    map[string]Host
	sessions map[string]SessionMeta
	entries  map[string]map[int64]Entry
	ended    map[string]bool
	beats    int
	inbox    []RemotePrompt
	acks     map[string]string
	failPost int // fail this many entry posts with 500
}

func newFake() *fakeServer {
	return &fakeServer{hosts: map[string]Host{}, sessions: map[string]SessionMeta{},
		entries: map[string]map[int64]Entry{}, ended: map[string]bool{}, acks: map[string]string{}}
}

func (f *fakeServer) lastSeq(id string) int64 {
	last := int64(-1)
	for s := range f.entries[id] {
		if s > last {
			last = s
		}
	}
	return last
}

func (f *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	p := strings.TrimPrefix(r.URL.Path, apiPath)
	parts := strings.Split(strings.Trim(p, "/"), "/")
	writeJSON := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	switch {
	case r.Method == http.MethodPut && parts[0] == "hosts":
		var h Host
		_ = json.NewDecoder(r.Body).Decode(&h)
		f.hosts[parts[1]] = h
		writeJSON(map[string]bool{"ok": true})
	case r.Method == http.MethodGet && parts[0] == "hosts" && parts[2] == "inbox":
		out := f.inbox
		f.inbox = nil
		writeJSON(map[string]any{"prompts": out})
	case r.Method == http.MethodPut && parts[0] == "sessions":
		var m SessionMeta
		_ = json.NewDecoder(r.Body).Decode(&m)
		f.sessions[parts[1]] = m
		delete(f.ended, parts[1])
		writeJSON(map[string]int64{"lastSeq": f.lastSeq(parts[1])})
	case r.Method == http.MethodPost && len(parts) == 3 && parts[2] == "entries":
		if f.failPost > 0 {
			f.failPost--
			http.Error(w, "boom", 500)
			return
		}
		var in struct{ Entries []Entry }
		_ = json.NewDecoder(r.Body).Decode(&in)
		if f.entries[parts[1]] == nil {
			f.entries[parts[1]] = map[int64]Entry{}
		}
		for _, e := range in.Entries {
			if _, ok := f.entries[parts[1]][e.Seq]; !ok {
				f.entries[parts[1]][e.Seq] = e
			}
		}
		writeJSON(map[string]int64{"lastSeq": f.lastSeq(parts[1])})
	case r.Method == http.MethodPost && len(parts) == 3 && parts[2] == "heartbeat":
		f.beats++
		writeJSON(map[string]bool{"ok": true})
	case r.Method == http.MethodPost && len(parts) == 3 && parts[2] == "end":
		f.ended[parts[1]] = true
		writeJSON(map[string]bool{"ok": true})
	case r.Method == http.MethodPost && parts[0] == "prompts":
		var in struct{ Status string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.acks[parts[1]] = in.Status
		writeJSON(map[string]bool{"ok": true})
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeServer) count(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.entries[id])
}

func startSyncer(t *testing.T, srv *httptest.Server, remote bool) *Syncer {
	t.Helper()
	c, err := NewClient(srv.URL+apiPath, func() (string, error) { return "ApiKey org:hex", nil }, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	s := New(Options{Client: c, HostID: testHost, Host: Host{Hostname: "box"}, RemotePrompts: remote,
		TickEvery: 20 * time.Millisecond, HeartbeatEvery: 50 * time.Millisecond, InboxWait: time.Second})
	s.Start(context.Background())
	t.Cleanup(func() { s.Close(time.Second) })
	return s
}

func appendLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l); err != nil {
			t.Fatal(err)
		}
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func line(id, typ, content string) string {
	b, _ := json.Marshal(map[string]any{"id": id, "type": typ, "content": content, "timestamp": 1})
	return string(b) + "\n"
}

// The website holds exactly the file's lines, keyed by line index, and a
// line still being written waits for its newline.
func TestMirrorsFileLineForLine(t *testing.T) {
	fake := newFake()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	path := filepath.Join(t.TempDir(), testSess+".jsonl")
	s := startSyncer(t, srv, false)

	s.Activate(SessionInfo{ID: testSess, Path: path, ProjectName: "belai"})
	appendLines(t, path,
		line("a", "session_name", "fix the bug"),
		line("b", "user", "hello"),
		"not json\n",
		`{"id":"d","type":"assistant","content":"hi","meta":{"model":"m1","provider":"p1"}}`+"\n",
		`{"id":"e","type":"user","content":"part`)
	s.Nudge()
	eventually(t, "4 lines", func() bool { return fake.count(testSess) == 4 })

	fake.mu.Lock()
	got := fake.entries[testSess]
	if got[0].Type != "session_name" || got[1].Content != "hello" || got[2].Type != "invalid" || got[3].ID != "d" {
		t.Fatalf("entries = %+v", got)
	}
	fake.mu.Unlock()

	appendLines(t, path, `ial"}`+"\n")
	s.Nudge()
	eventually(t, "the completed line", func() bool { return fake.count(testSess) == 5 })

	// Metadata from the file reaches the session registration.
	eventually(t, "meta", func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		m := fake.sessions[testSess]
		return m.Name == "fix the bug" && m.Model == "m1" && m.Provider == "p1" && m.HostID == testHost
	})
	eventually(t, "heartbeat", func() bool { fake.mu.Lock(); defer fake.mu.Unlock(); return fake.beats > 0 })
	for _, a := range fake.auth {
		if a != "ApiKey org:hex" {
			t.Fatalf("auth header %q", a)
		}
	}
}

// A restarted host resumes from the server's mark and a failed upload is
// retried without duplicating anything.
func TestResumesFromServerMarkAndRetries(t *testing.T) {
	fake := newFake()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	path := filepath.Join(t.TempDir(), testSess+".jsonl")
	appendLines(t, path, line("a", "user", "1"), line("b", "assistant", "2"), line("c", "user", "3"))
	fake.entries[testSess] = map[int64]Entry{0: {Seq: 0, ID: "a", Type: "user"}, 1: {Seq: 1, ID: "b", Type: "assistant"}}
	fake.failPost = 1

	s := startSyncer(t, srv, false)
	s.Activate(SessionInfo{ID: testSess, Path: path})
	eventually(t, "the missing line", func() bool { return fake.count(testSess) == 3 })
	fake.mu.Lock()
	if fake.entries[testSess][2].ID != "c" {
		t.Fatalf("seq 2 = %+v", fake.entries[testSess][2])
	}
	fake.mu.Unlock()
}

// Switching session ends the previous one; Close ends the current one.
func TestSwitchAndCloseEndSessions(t *testing.T) {
	fake := newFake()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	dir := t.TempDir()
	const second = "33333333-3333-4333-8333-333333333333"
	p1, p2 := filepath.Join(dir, "1.jsonl"), filepath.Join(dir, "2.jsonl")
	appendLines(t, p1, line("a", "user", "1"))
	appendLines(t, p2, line("b", "summary", "s"))

	s := startSyncer(t, srv, false)
	s.Activate(SessionInfo{ID: testSess, Path: p1})
	eventually(t, "first session", func() bool { return fake.count(testSess) == 1 })
	s.Activate(SessionInfo{ID: second, Path: p2, ParentSessionID: testSess})
	eventually(t, "first ended", func() bool { fake.mu.Lock(); defer fake.mu.Unlock(); return fake.ended[testSess] })
	eventually(t, "second session", func() bool { return fake.count(second) == 1 })
	s.Close(time.Second)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if !fake.ended[second] {
		t.Fatal("Close did not end the live session")
	}
	if fake.sessions[second].ParentSessionID != testSess {
		t.Fatalf("parent = %q", fake.sessions[second].ParentSessionID)
	}
}

// A session with no lines is never registered.
func TestEmptySessionNeverRegistered(t *testing.T) {
	fake := newFake()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	s := startSyncer(t, srv, false)
	s.Activate(SessionInfo{ID: testSess, Path: filepath.Join(t.TempDir(), "none.jsonl")})
	time.Sleep(150 * time.Millisecond)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.sessions) != 0 {
		t.Fatalf("registered %v", fake.sessions)
	}
}

func TestInboxDeliversPromptsAndAcks(t *testing.T) {
	fake := newFake()
	fake.inbox = []RemotePrompt{{ID: "p1", SessionID: testSess, Content: "run tests"}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	path := filepath.Join(t.TempDir(), testSess+".jsonl")
	appendLines(t, path, line("a", "user", "1"))

	s := startSyncer(t, srv, true)
	s.Activate(SessionInfo{ID: testSess, Path: path})
	select {
	case p := <-s.Prompts():
		if p.ID != "p1" || p.Content != "run tests" {
			t.Fatalf("prompt = %+v", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no prompt delivered")
	}
	s.Ack("p1", AckAccepted, "", "e1")
	eventually(t, "ack", func() bool { fake.mu.Lock(); defer fake.mu.Unlock(); return fake.acks["p1"] == AckAccepted })
}

func TestAllowedOrigin(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://www.vulnetix.com/api/site/v1/belai": true,
		"https://vulnetix.com":                       true,
		"http://www.vulnetix.com":                    false,
		"https://vulnetix.com.evil.io":               false,
		"https://evilvulnetix.com":                   false,
		"https://user@www.vulnetix.com":              false,
		"http://localhost:5173":                      true,
		"http://127.0.0.1:3000":                      true,
		"http://10.0.0.1":                            false,
	} {
		if got := AllowedOrigin(raw); got != want {
			t.Errorf("AllowedOrigin(%q) = %v, want %v", raw, got, want)
		}
	}
	if _, err := NewClient("https://example.com", nil, nil); err == nil {
		t.Fatal("NewClient accepted a non-Vulnetix origin")
	}
}

func TestUsableCredential(t *testing.T) {
	if UsableCredential("ApiKey o:k") != nil || UsableCredential("Bearer a.b.c") != nil {
		t.Fatal("rejected a usable credential")
	}
	if !errors.Is(UsableCredential("Bearer opaque"), ErrTokenCredential) {
		t.Fatal("accepted an opaque token")
	}
}

func TestCleanPrompt(t *testing.T) {
	in := "run\x1b[2J the\r\n tests‮ now\x00 <system nonce=\"x\">hi</system>"
	got := CleanPrompt(in)
	if strings.ContainsAny(got, "\x1b\x00\r‮") {
		t.Fatalf("control runes survived: %q", got)
	}
	if !strings.Contains(got, "\n") || !strings.HasPrefix(got, "run[2J the") {
		t.Fatalf("CleanPrompt = %q", got)
	}
	if len(CleanPrompt(strings.Repeat("x", MaxPromptBytes+10))) != MaxPromptBytes {
		t.Fatal("not capped")
	}
}

func TestHostIDStable(t *testing.T) {
	dir := t.TempDir()
	a, err := HostID(dir)
	if err != nil || !uuidPattern.MatchString(a) {
		t.Fatalf("HostID = %q, %v", a, err)
	}
	b, _ := HostID(dir)
	if a != b {
		t.Fatalf("host id changed: %s → %s", a, b)
	}
	if got := identifier("my<box>\x1b.local", 64); got != "mybox.local" {
		t.Fatalf("identifier = %q", got)
	}
}
