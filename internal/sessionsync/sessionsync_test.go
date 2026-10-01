package sessionsync

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/docparity"
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
	answers  []RemoteAnswer
	drafts   []RemoteDraft
	// results holds each posted draft result body, by draft id.
	results map[string]map[string]any
	// conflictDrafts answers a draft result with 409 (cancelled on the web).
	conflictDrafts bool
	acks           map[string]string
	gzipped        int // entry posts that arrived gzip-encoded
	failPost       int // fail this many entry posts with 500
}

func newFake() *fakeServer {
	return &fakeServer{hosts: map[string]Host{}, sessions: map[string]SessionMeta{},
		entries: map[string]map[int64]Entry{}, ended: map[string]bool{}, acks: map[string]string{},
		results: map[string]map[string]any{}}
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
		out, ans, drafts := f.inbox, f.answers, f.drafts
		f.inbox, f.answers, f.drafts = nil, nil, nil
		writeJSON(map[string]any{"prompts": out, "answers": ans, "drafts": drafts})
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
		var body io.Reader = r.Body
		if r.Header.Get("Content-Encoding") == "gzip" {
			zr, err := gzip.NewReader(r.Body)
			if err != nil {
				http.Error(w, "bad gzip", 400)
				return
			}
			f.gzipped++
			body = zr
		}
		_ = json.NewDecoder(body).Decode(&in)
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
	case r.Method == http.MethodPost && parts[0] == "agent-drafts":
		if f.conflictDrafts {
			http.Error(w, `{"error":"cancelled"}`, http.StatusConflict)
			return
		}
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.results[parts[1]] = in
		writeJSON(map[string]bool{"ok": true})
	case r.Method == http.MethodPost && parts[0] == "answers":
		var in struct{ Status string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.acks["answer:"+parts[1]] = in.Status
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

// The profile a session runs under rides the registration, and a later
// session_meta line (ctrl+p picked another agent) re-registers the session.
func TestActiveProfileReachesTheRegistration(t *testing.T) {
	fake := newFake()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	path := filepath.Join(t.TempDir(), testSess+".jsonl")
	profile := func() string {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return fake.sessions[testSess].ActiveProfile
	}
	s := startSyncer(t, srv, false)

	s.Activate(SessionInfo{ID: testSess, Path: path})
	appendLines(t, path, line("a", "session_meta", `{"schema":2,"activeProfile":"belai:patcher"}`))
	s.Nudge()
	eventually(t, "the first profile", func() bool { return profile() == "belai:patcher" })

	appendLines(t, path, line("b", "session_meta", `{"schema":2,"activeProfile":"belai:verifier"}`))
	s.Nudge()
	eventually(t, "the switched profile", func() bool { return profile() == "belai:verifier" })

	// A line that names no profile leaves it alone.
	appendLines(t, path, line("c", "session_meta", `{"schema":2,"mode":"agent"}`))
	s.Nudge()
	eventually(t, "the mode update", func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return fake.sessions[testSess].Mode == "agent"
	})
	if got := profile(); got != "belai:verifier" {
		t.Fatalf("profile after a mode-only line = %q", got)
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

// A session file that was never created is never registered. The TUI creates
// the live session's file at startup; other callers may point at a missing
// file, which stays absent from the website.
func TestMissingSessionFileNeverRegistered(t *testing.T) {
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

// An empty session file that exists (the TUI creates it at startup) is
// registered immediately, before its first line.
func TestEmptySessionFileRegistered(t *testing.T) {
	fake := newFake()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	path := filepath.Join(t.TempDir(), testSess+".jsonl")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s := startSyncer(t, srv, false)
	s.Activate(SessionInfo{ID: testSess, Path: path, ProjectName: "belai"})
	eventually(t, "the empty session", func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		_, ok := fake.sessions[testSess]
		return ok
	})
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.sessions[testSess].ProjectName != "belai" {
		t.Fatalf("session meta = %+v", fake.sessions[testSess])
	}
	if fake.lastSeq(testSess) != -1 {
		t.Fatalf("lastSeq = %d, want -1 for an empty file", fake.lastSeq(testSess))
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
	if !strings.Contains(got, "\n") || !strings.HasPrefix(got, "run the") {
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

// Web answers come through the same inbox as prompts, and an accepted answer
// is acked only after the host's ask_answer line is uploaded.
func TestInboxDeliversAnswersAndAcks(t *testing.T) {
	fake := newFake()
	fake.answers = []RemoteAnswer{{ID: "a1", SessionID: testSess, AskID: "k1", Kind: "permission",
		Payload: json.RawMessage(`{"decision":"deny"}`)}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	path := filepath.Join(t.TempDir(), testSess+".jsonl")
	appendLines(t, path, line("a", "user", "1"))

	c, err := NewClient(srv.URL+apiPath, func() (string, error) { return "ApiKey org:hex", nil }, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	s := New(Options{Client: c, HostID: testHost, RemoteAnswers: true,
		TickEvery: 20 * time.Millisecond, HeartbeatEvery: time.Second, InboxWait: time.Second})
	s.Start(context.Background())
	t.Cleanup(func() { s.Close(time.Second) })
	s.Activate(SessionInfo{ID: testSess, Path: path})
	select {
	case a := <-s.Answers():
		if a.ID != "a1" || a.AskID != "k1" || string(a.Payload) != `{"decision":"deny"}` {
			t.Fatalf("answer = %+v", a)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no answer delivered")
	}
	eventually(t, "session registered", func() bool { fake.mu.Lock(); defer fake.mu.Unlock(); return fake.sessions[testSess].RemoteAnswers })
	s.AckAnswer("a1", AckAccepted, "", "answer-k1")
	eventually(t, "answer ack", func() bool { fake.mu.Lock(); defer fake.mu.Unlock(); return fake.acks["answer:a1"] == AckAccepted })
}

// A host that takes no web answers refuses any the server hands it.
func TestAnswersRefusedWhenOff(t *testing.T) {
	fake := newFake()
	fake.answers = []RemoteAnswer{{ID: "a2", SessionID: testSess, AskID: "k2", Kind: "clarify"}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	path := filepath.Join(t.TempDir(), testSess+".jsonl")
	appendLines(t, path, line("a", "user", "1"))
	s := startSyncer(t, srv, true)
	s.Activate(SessionInfo{ID: testSess, Path: path})
	eventually(t, "refusal", func() bool { fake.mu.Lock(); defer fake.mu.Unlock(); return fake.acks["answer:a2"] == AckRefused })
}

// Large batches go up gzip-encoded and arrive intact.
func TestLargeBatchesAreGzipped(t *testing.T) {
	fake := newFake()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	path := filepath.Join(t.TempDir(), testSess+".jsonl")
	big := strings.Repeat("diff row text ", 2000)
	appendLines(t, path, line("a", "tool", big), line("b", "user", "small"))
	s := startSyncer(t, srv, false)
	s.Activate(SessionInfo{ID: testSess, Path: path})
	eventually(t, "upload", func() bool { return fake.count(testSess) == 2 })
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.gzipped == 0 {
		t.Fatal("a large batch was sent uncompressed")
	}
	if fake.entries[testSess][0].Content != big {
		t.Fatal("gzipped content did not round-trip")
	}
}

// A test binary never gets a client for a real origin, whatever the
// environment says, so a test that forgot to isolate VULNETIX_WEB_URL
// cannot push its fixtures to the developer's board. Loopback still works
// for tests that run a local server.
func TestNewClientRefusesRealOriginUnderTest(t *testing.T) {
	auth := func() (string, error) { return "Bearer x", nil }
	for _, base := range []string{BaseURL(""), "https://staging.vulnetix.com" + apiPath} {
		if _, err := NewClient(base, auth, nil); !errors.Is(err, errTestOrigin) {
			t.Fatalf("NewClient(%s) under test = %v, want errTestOrigin", base, err)
		}
	}
	for _, base := range []string{"http://127.0.0.1:1" + apiPath, "http://localhost:8080" + apiPath, "http://[::1]:9" + apiPath} {
		if _, err := NewClient(base, auth, nil); err != nil {
			t.Fatalf("NewClient(%s) = %v, want a loopback client", base, err)
		}
	}
}

// A session that is activated and closed straight away still reaches the
// server: Close takes what was queued ahead of it, registers the session,
// uploads its lines and ends it. The tick is an hour, so only that path can.
func TestCloseFlushesASessionActivatedJustBefore(t *testing.T) {
	for i := 0; i < 20; i++ {
		fake := newFake()
		srv := httptest.NewServer(fake)
		path := filepath.Join(t.TempDir(), testSess+".jsonl")
		appendLines(t, path, line("a", "user", "hi"), line("b", "assistant", "done"))
		c, err := NewClient(srv.URL+apiPath, func() (string, error) { return "ApiKey org:hex", nil }, srv.Client())
		if err != nil {
			t.Fatal(err)
		}
		s := New(Options{Client: c, HostID: testHost, Host: Host{Hostname: "box"},
			TickEvery: time.Hour, HeartbeatEvery: time.Hour, InboxWait: time.Second})
		s.Start(context.Background())
		s.Activate(SessionInfo{ID: testSess, Path: path})
		s.Nudge()
		s.Close(5 * time.Second)
		fake.mu.Lock()
		registered := len(fake.sessions)
		ended := fake.ended[testSess]
		fake.mu.Unlock()
		if registered != 1 || fake.count(testSess) != 2 || !ended {
			srv.Close()
			t.Fatalf("round %d: registered %d, %d lines, ended %v; want 1, 2, true", i, registered, fake.count(testSess), ended)
		}
		srv.Close()
	}
}

// TestSessionSyncDocNamesTheActiveProfileRules keeps the rules docs/session-sync.md
// states for activeProfile equal to the code: the wire field, its omitempty (so a
// cleared agent is never sent), and the cap the page gives for the server.
func TestSessionSyncDocNamesTheActiveProfileRules(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/session-sync.md")), " ")

	f, ok := reflect.TypeOf(SessionMeta{}).FieldByName("ActiveProfile")
	if !ok {
		t.Fatal("SessionMeta has no ActiveProfile")
	}
	tag := f.Tag.Get("json")
	if tag != "activeProfile,omitempty" {
		t.Fatalf("the wire tag is %q; the page says it is activeProfile and omitempty", tag)
	}
	for _, want := range []string{
		"`activeProfile`", "`omitempty`", "session.LatestMeta", "caps it at 128 bytes",
		"clearing the agent never reaches the transcript", "`<profile> · <card> <title>`",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/session-sync.md no longer says %q", want)
		}
	}
	if _, ok := reflect.TypeOf(SessionInfo{}).FieldByName("ActiveProfile"); !ok {
		t.Error("SessionInfo has no ActiveProfile, so the tail cannot carry it")
	}
}

// A profile cleared on the host never reaches the website: the lines that name
// none leave the registered one alone, and a re-activation of the same session
// (a resume) keeps what the file said before.
func TestActiveProfileSurvivesALineThatNamesNone(t *testing.T) {
	fake := newFake()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	path := filepath.Join(t.TempDir(), testSess+".jsonl")
	profile := func() string {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return fake.sessions[testSess].ActiveProfile
	}
	s := startSyncer(t, srv, false)

	s.Activate(SessionInfo{ID: testSess, Path: path})
	appendLines(t, path, line("a", "session_meta", `{"schema":2,"activeProfile":"belai:scout"}`))
	s.Nudge()
	eventually(t, "the profile", func() bool { return profile() == "belai:scout" })

	// ctrl+p cleared the agent: the transcript line omits the field.
	appendLines(t, path, line("b", "session_meta", `{"schema":2,"cwd":"/src/x"}`), "not json\n")
	s.Nudge()
	eventually(t, "the cwd update", func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return fake.sessions[testSess].Cwd == "/src/x"
	})
	if got := profile(); got != "belai:scout" {
		t.Fatalf("profile = %q after a line that names none", got)
	}

	// Activating the same session again (a resume) re-reads the whole file and keeps it.
	s.Activate(SessionInfo{ID: testSess, Path: path})
	s.Nudge()
	time.Sleep(150 * time.Millisecond)
	if got := profile(); got != "belai:scout" {
		t.Fatalf("profile = %q after the session was activated again", got)
	}
}

// TestSessionSyncDocNamesEveryRecordTheTailReads keeps the "What the session
// records" table equal to the lines the syncer reads display metadata from:
// each type tail.observe switches on, and the registration field it feeds, is on the page.
func TestSessionSyncDocNamesEveryRecordTheTailReads(t *testing.T) {
	doc := docparity.Read(t, "docs/session-sync.md")
	for _, typ := range []string{"session_name", "session_meta", "assistant"} {
		// A row is "| `type` |"; assistant lines are the conversation itself and
		// are described by the model and provider they carry.
		if typ == "assistant" {
			continue
		}
		if !strings.Contains(doc, "| `"+typ+"` |") {
			t.Errorf("the records table has no row for %s, which the syncer reads", typ)
		}
	}
	// What the tail does with a record: the same statement the page makes.
	src, err := os.ReadFile("syncer.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"activeProfile", "resumedFrom", "cwd", "mode"} {
		if !strings.Contains(string(src), `json:"`+field+`"`) {
			t.Errorf("tail.observe no longer reads %s from session_meta, but the page says it does", field)
		}
		if !strings.Contains(doc, "`"+field+"`") {
			t.Errorf("the page does not name %s", field)
		}
	}

	// A session_name line sets the registered name, the latest wins, and an empty one never clears it.
	tl := &tail{}
	tl.observe(Entry{Type: "session_name", Content: "first name"})
	tl.observe(Entry{Type: "session_name", Content: "  second name "})
	tl.observe(Entry{Type: "session_name", Content: "   "})
	if tl.info.Name != "second name" {
		t.Errorf("name = %q, want the latest non-empty one, trimmed", tl.info.Name)
	}
}

// TestSessionSyncDocNamesTheLifecycleRules holds the lifecycle bullets on
// docs/session-sync.md to the numbers and tests behind them: the heartbeat and
// liveness windows, and the tests that pin a session that ends at once and an
// empty or missing file.
func TestSessionSyncDocNamesTheLifecycleRules(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/session-sync.md")), " ")
	// The page says a heartbeat every 15 s and live under 45 s; the syncer's default beat is the first.
	src, err := os.ReadFile("syncer.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "15 * time.Second") || !strings.Contains(doc, "heartbeat every 15 s") || !strings.Contains(doc, "under 45 s old") {
		t.Error("the heartbeat interval or the 45 s liveness window on the page no longer matches the syncer")
	}
	for _, want := range []string{
		"TestCloseFlushesASessionActivatedJustBefore",
		"registers the session if it is not yet, uploads every line and ends it",
		"An empty session file is registered",
		"A session whose file is never created",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/session-sync.md no longer says %q", want)
		}
	}
	// Every test the page names exists.
	for _, name := range regexp.MustCompile("`(Test[A-Za-z]+)`").FindAllStringSubmatch(doc, -1) {
		found := false
		for _, f := range []string{"sessionsync_test.go"} {
			b, err := os.ReadFile(f)
			if err == nil && strings.Contains(string(b), "func "+name[1]+"(") {
				found = true
			}
		}
		if !found {
			t.Errorf("docs/session-sync.md names %s, which sessionsync_test.go does not define", name[1])
		}
	}
}
