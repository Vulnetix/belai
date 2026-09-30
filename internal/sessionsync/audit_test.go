package sessionsync

import (
	"compress/gzip"
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

	"github.com/vulnetix/belai/internal/audit"
)

// auditFake is an in-memory /v1/belai audit endpoint with the server's rules:
// a seq it holds is ignored, each new event must continue the chain, and a
// stream's cursor is returned to the host.
type auditFake struct {
	mu      sync.Mutex
	streams map[string]*auditFakeStream
	posts   []int // events in each POST
	gzipped int
	fail    int // fail this many posts with 500
	hosts   int
}

type auditFakeStream struct {
	events []audit.Event
	ok     bool
}

func newAuditFake() *auditFake { return &auditFake{streams: map[string]*auditFakeStream{}} }

func (f *auditFake) cursor(s *auditFakeStream) AuditCursor {
	c := AuditCursor{LastSeq: -1, ChainOK: s.ok}
	if n := len(s.events); n > 0 {
		c.LastSeq, c.LastHash = s.events[n-1].Seq, s.events[n-1].Hash
	}
	return c
}

func (f *auditFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, apiPath), "/"), "/")
	write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	switch {
	case r.Method == http.MethodPut && len(parts) == 2 && parts[0] == "hosts":
		f.hosts++
		write(map[string]bool{"ok": true})
	case r.Method == http.MethodPut && len(parts) == 4 && parts[2] == "audit":
		s := f.streams[parts[3]]
		if s == nil {
			s = &auditFakeStream{ok: true}
			f.streams[parts[3]] = s
		}
		write(f.cursor(s))
	case r.Method == http.MethodPost && len(parts) == 4 && parts[2] == "audit":
		if f.fail > 0 {
			f.fail--
			http.Error(w, "boom", 500)
			return
		}
		s := f.streams[parts[3]]
		if s == nil {
			http.NotFound(w, r)
			return
		}
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
		var in struct {
			Events []audit.Event `json:"events"`
		}
		dec := json.NewDecoder(body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil || len(in.Events) > 200 {
			http.Error(w, "bad body", 400)
			return
		}
		f.posts = append(f.posts, len(in.Events))
		for _, e := range in.Events {
			if n := len(s.events); n > 0 && e.Seq <= s.events[n-1].Seq {
				continue
			}
			prev, want := "", int64(0)
			if n := len(s.events); n > 0 {
				prev, want = s.events[n-1].Hash, s.events[n-1].Seq+1
			}
			if e.Seq != want || e.Prev != prev || audit.HashOf(e) != e.Hash {
				s.ok = false
			}
			s.events = append(s.events, e)
		}
		write(f.cursor(s))
	default:
		http.NotFound(w, r)
	}
}

func (f *auditFake) stream(id string) *auditFakeStream {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s := f.streams[id]; s != nil {
		cp := *s
		cp.events = append([]audit.Event(nil), s.events...)
		return &cp
	}
	return nil
}

func auditTestClient(t *testing.T) (*Client, *auditFake) {
	t.Helper()
	fake := newAuditFake()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	c, err := NewClient(srv.URL+apiPath, func() (string, error) { return "ApiKey test:sig", nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c, fake
}

func auditTestSyncer(t *testing.T, c *Client) (*AuditSyncer, *audit.Recorder, string) {
	t.Helper()
	state := t.TempDir()
	rec, err := audit.Open(state, "box")
	if err != nil {
		t.Fatal(err)
	}
	s := NewAudit(AuditOptions{Client: c, HostID: testHost, Host: Host{Hostname: "box", OS: "linux", BelaiVersion: "1.0.0"},
		Recorder: rec, StateDir: state, TickEvery: 20 * time.Millisecond, BackoffMin: 10 * time.Millisecond})
	return s, rec, state
}

func auditWaitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func emitN(n int, kind audit.Kind) {
	for i := 0; i < n; i++ {
		audit.Emit(audit.Fact{Kind: kind, ActorKind: audit.ActorAgent, Actor: "delivery", ItemID: "K-000001"})
	}
}

func installRecorder(t *testing.T, rec *audit.Recorder) {
	t.Helper()
	audit.SetDefault(rec)
	t.Cleanup(func() { audit.SetDefault(nil) })
}

func TestAuditUploadsInOrderAndChains(t *testing.T) {
	c, fake := auditTestClient(t)
	s, rec, _ := auditTestSyncer(t, c)
	installRecorder(t, rec)
	s.Start(context.Background())
	defer s.Close(time.Second)

	emitN(5, audit.WorkerState)
	auditWaitFor(t, "5 events on the server", func() bool {
		st := fake.stream(rec.StreamID())
		return st != nil && len(st.events) == 5
	})
	st := fake.stream(rec.StreamID())
	if !st.ok || audit.Verify(st.events) != -1 {
		t.Fatalf("the server's copy must verify: ok=%v break=%d", st.ok, audit.Verify(st.events))
	}
	auditWaitFor(t, "status", func() bool { return s.Status().LastSeq == 4 })
	if s := s.Status(); !s.ChainOK || s.LastError != "" {
		t.Fatalf("status = %+v", s)
	}
	if fake.hosts < 1 {
		t.Fatal("the host must be registered before its stream")
	}
}

func TestAuditResumesFromTheServersCursor(t *testing.T) {
	c, fake := auditTestClient(t)
	s, rec, _ := auditTestSyncer(t, c)
	installRecorder(t, rec)
	emitN(6, audit.CardClaimed)

	// The server already holds the first three events of this stream.
	fake.streams[rec.StreamID()] = &auditFakeStream{ok: true}
	ev := readAuditFile(t, rec.Path())
	fake.streams[rec.StreamID()].events = append(fake.streams[rec.StreamID()].events, ev[:3]...)

	s.Start(context.Background())
	defer s.Close(time.Second)
	auditWaitFor(t, "the rest", func() bool { st := fake.stream(rec.StreamID()); return len(st.events) == 6 })

	fake.mu.Lock()
	defer fake.mu.Unlock()
	total := 0
	for _, n := range fake.posts {
		total += n
	}
	if total != 3 {
		t.Fatalf("uploaded %d events after the cursor, want only the 3 it lacked (%v)", total, fake.posts)
	}
	if !fake.streams[rec.StreamID()].ok {
		t.Fatal("a resumed upload must continue the chain")
	}
}

func TestAuditBatchesAndCompresses(t *testing.T) {
	c, fake := auditTestClient(t)
	s, rec, _ := auditTestSyncer(t, c)
	installRecorder(t, rec)
	// Long values make the batch cross the gzip threshold.
	long := strings.Repeat("a", 250)
	for i := 0; i < 250; i++ {
		audit.Emit(audit.Fact{Kind: audit.GateVerified, ActorKind: audit.ActorHarness, Actor: long, Repo: long, Branch: long})
	}
	s.Start(context.Background())
	defer s.Close(time.Second)
	auditWaitFor(t, "250 events", func() bool { st := fake.stream(rec.StreamID()); return st != nil && len(st.events) == 250 })

	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, n := range fake.posts {
		if n > maxAuditBatchEvents {
			t.Fatalf("a POST carried %d events, cap %d", n, maxAuditBatchEvents)
		}
	}
	if len(fake.posts) < 3 {
		t.Fatalf("250 events went in %d POSTs", len(fake.posts))
	}
	if fake.gzipped == 0 {
		t.Fatal("a large batch must be gzipped")
	}
}

func TestAuditRetriesAfterAFailure(t *testing.T) {
	c, fake := auditTestClient(t)
	fake.fail = 2
	s, rec, _ := auditTestSyncer(t, c)
	installRecorder(t, rec)
	emitN(3, audit.RepoCommit)
	s.Start(context.Background())
	defer s.Close(time.Second)
	auditWaitFor(t, "the events after two failures", func() bool {
		st := fake.stream(rec.StreamID())
		return st != nil && len(st.events) == 3
	})
	if st := fake.stream(rec.StreamID()); !st.ok {
		t.Fatal("a retried upload must not break the chain")
	}
}

func TestAuditCloseFlushesAndRemovesItsFile(t *testing.T) {
	c, fake := auditTestClient(t)
	s, rec, _ := auditTestSyncer(t, c)
	installRecorder(t, rec)
	s.opts.TickEvery = time.Hour // only Close can upload
	s.Start(context.Background())
	emitN(4, audit.WorkerStopped)
	s.Close(2 * time.Second)

	if st := fake.stream(rec.StreamID()); st == nil || len(st.events) != 4 {
		t.Fatalf("Close must flush: %+v", st)
	}
	if _, err := os.Stat(rec.Path()); !os.IsNotExist(err) {
		t.Fatalf("a fully uploaded stream file must be removed: %v", err)
	}
}

func TestAuditCloseKeepsAFileTheServerDoesNotHold(t *testing.T) {
	c, _ := auditTestClient(t)
	// A server that cannot be reached keeps the file for the next run.
	c.Base = "http://127.0.0.1:1" + apiPath
	s, rec, _ := auditTestSyncer(t, c)
	installRecorder(t, rec)
	s.opts.TickEvery = time.Hour
	s.Start(context.Background())
	emitN(2, audit.WorkerStopped)
	s.Close(time.Second)
	if _, err := os.Stat(rec.Path()); err != nil {
		t.Fatalf("an unsent stream must stay on disk: %v", err)
	}
}

func TestAuditSweepFinishesOrphansAndSparesLiveProcesses(t *testing.T) {
	c, fake := auditTestClient(t)
	s, rec, state := auditTestSyncer(t, c)
	installRecorder(t, rec)

	dir := audit.Dir(state)
	// An orphan: a crashed process (a pid that cannot be running) left 3 events.
	orphanID := "55555555-5555-4555-8555-555555555555"
	writeAuditFile(t, filepath.Join(dir, "999999999."+orphanID+".jsonl"), 3)
	// An empty orphan is deleted without a request.
	emptyID := "66666666-6666-4666-8666-666666666666"
	emptyPath := filepath.Join(dir, "999999998."+emptyID+".jsonl")
	if err := os.WriteFile(emptyPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// A live process's stream (this test's own pid) must be left alone.
	liveID := "77777777-7777-4777-8777-777777777777"
	livePath := filepath.Join(dir, itoa(os.Getpid())+"."+liveID+".jsonl")
	writeAuditFile(t, livePath, 2)
	// A file that is not a stream is ignored.
	strayPath := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(strayPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	s.Start(context.Background())
	defer s.Close(time.Second)
	emitN(1, audit.WorkerStarted) // gets the loop to step, then sweep
	auditWaitFor(t, "the orphan on the server", func() bool {
		st := fake.stream(orphanID)
		return st != nil && len(st.events) == 3
	})
	auditWaitFor(t, "the orphan file to go", func() bool {
		_, err := os.Stat(filepath.Join(dir, "999999999."+orphanID+".jsonl"))
		return os.IsNotExist(err)
	})
	if st := fake.stream(orphanID); !st.ok {
		t.Fatal("an orphan's chain must survive the sweep")
	}
	if _, err := os.Stat(emptyPath); !os.IsNotExist(err) {
		t.Fatal("an empty orphan must be removed")
	}
	if st := fake.stream(emptyID); st != nil {
		t.Fatal("an empty orphan must not reach the server")
	}
	if _, err := os.Stat(livePath); err != nil {
		t.Fatalf("a live process's stream was touched: %v", err)
	}
	if st := fake.stream(liveID); st != nil {
		t.Fatal("a live process's stream was uploaded by someone else")
	}
	if _, err := os.Stat(strayPath); err != nil {
		t.Fatalf("a stray file was removed: %v", err)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// writeAuditFile writes n chained events the way a Recorder would.
func writeAuditFile(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	prev := ""
	for i := 0; i < n; i++ {
		e := audit.Event{Seq: int64(i), At: 1790000000000 + int64(i), Scope: "agent", Kind: "card.claimed",
			ActorKind: "agent", Actor: "delivery", Prev: prev}
		e.Hash = audit.HashOf(e)
		prev = e.Hash
		line, _ := json.Marshal(e)
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readAuditFile(t *testing.T, path string) []audit.Event {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []audit.Event
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var e audit.Event
		if err := json.Unmarshal([]byte(l), &e); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}
