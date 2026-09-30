package rc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/turnlog"
)

const testHost = "11111111-1111-4111-8111-111111111111"

func TestPreflightListsEveryFix(t *testing.T) {
	off := false
	cs := Preflight(context.Background(), PreflightOptions{
		Settings:   config.Settings{Guardrails: &off},
		LookPath:   func(string) (string, error) { return "", errors.New("missing") },
		AuthHeader: func(string) (string, error) { return "", errors.New("none") },
	})
	if cs.OK() {
		t.Fatal("preflight passed with no CLI, no login, guardrails off and no dirs")
	}
	for _, name := range []string{CheckCLI, CheckCredential, CheckGuardrails, CheckDirs} {
		c, ok := cs.Find(name)
		if !ok || c.Level != Fail || c.Fix == "" {
			t.Errorf("%s = %+v, want a failure with a fix", name, c)
		}
	}
	out := cs.Render()
	if !strings.Contains(out, "vulnetix auth login") {
		t.Errorf("render lacks the login fix:\n%s", out)
	}
}

func TestPreflightTokenLoginIsRefused(t *testing.T) {
	cs := Preflight(context.Background(), PreflightOptions{
		Dirs:       []Dir{{Path: "/x"}},
		LookPath:   func(string) (string, error) { return "/bin/vulnetix", nil },
		AuthHeader: func(string) (string, error) { return "Bearer opaque-token", nil },
	})
	c, _ := cs.Find(CheckCredential)
	if c.Level != Fail || !strings.Contains(c.Fix, "not --token") {
		t.Fatalf("token login = %+v", c)
	}
	// A missing CLI is only a warning when a credential resolves anyway.
	cs = Preflight(context.Background(), PreflightOptions{
		Dirs:       []Dir{{Path: "/x"}},
		LookPath:   func(string) (string, error) { return "", errors.New("missing") },
		AuthHeader: func(string) (string, error) { return "ApiKey org:secret", nil },
	})
	if c, _ := cs.Find(CheckCLI); c.Level != Warn {
		t.Fatalf("cli with a credential = %+v, want warn", c)
	}
	if !cs.OK() {
		t.Fatalf("preflight failed:\n%s", cs.Render())
	}
}

func TestAllowedIsExactAfterSymlinks(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "proj")
	sub := filepath.Join(proj, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(proj, link); err != nil {
		t.Fatal(err)
	}
	real, _ := Normalize(proj)
	dirs := []Dir{{Path: real}}
	if _, ok := Allowed(dirs, proj); !ok {
		t.Fatal("offered directory refused")
	}
	if _, ok := Allowed(dirs, link); !ok {
		t.Fatal("a symlink to the offered directory refused")
	}
	for _, bad := range []string{sub, root, proj + "-other", filepath.Join(proj, ".."), "relative"} {
		if _, ok := Allowed(dirs, bad); ok {
			t.Errorf("%s allowed", bad)
		}
	}
	// A symlink inside an offered directory pointing out of it is not it.
	out := t.TempDir()
	esc := filepath.Join(proj, "escape")
	if err := os.Symlink(out, esc); err != nil {
		t.Fatal(err)
	}
	if _, ok := Allowed(dirs, esc); ok {
		t.Error("escaping symlink allowed")
	}
}

// fakeSite is the rc half of /api/site/v1/belai.
type fakeSite struct {
	mu        sync.Mutex
	host      sessionsync.Host
	queue     []sessionsync.Dispatch
	acks      map[string][3]string // id → status, session, reason
	beats     int
	puts      int
	workers   []sessionsync.RCWorker
	offline   bool
	delivered chan struct{}
}

func (f *fakeSite) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := strings.TrimPrefix(r.URL.Path, "/api/site/v1/belai")
	switch {
	case r.Method == http.MethodPut && p == "/hosts/"+testHost:
		_ = json.NewDecoder(r.Body).Decode(&f.host)
		f.puts++
		w.Write([]byte(`{"ok":true}`))
	case p == "/hosts/"+testHost+"/rc/heartbeat":
		f.beats++
		var in struct {
			Workers []sessionsync.RCWorker `json:"workers"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.workers = in.Workers
		w.Write([]byte(`{"ok":true}`))
	case p == "/hosts/"+testHost+"/rc/offline":
		f.offline = true
		w.Write([]byte(`{"ok":true}`))
	case p == "/hosts/"+testHost+"/dispatch":
		out := f.queue
		f.queue = nil
		if len(out) == 0 {
			f.mu.Unlock()
			time.Sleep(20 * time.Millisecond)
			f.mu.Lock()
		}
		json.NewEncoder(w).Encode(map[string]any{"dispatches": out})
	case strings.HasPrefix(p, "/dispatches/") && strings.HasSuffix(p, "/ack"):
		id := strings.TrimSuffix(strings.TrimPrefix(p, "/dispatches/"), "/ack")
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.acks[id] = [3]string{in["status"], in["sessionId"], in["reason"]}
		w.Write([]byte(`{"ok":true}`))
		select {
		case f.delivered <- struct{}{}:
		default:
		}
	default:
		http.NotFound(w, r)
	}
}

func TestDaemonStartsRefusesAndStops(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	proj := t.TempDir()
	real, _ := Normalize(proj)
	site := &fakeSite{acks: map[string][3]string{}, delivered: make(chan struct{}, 8)}
	srv := httptest.NewServer(site)
	defer srv.Close()
	client, err := sessionsync.NewClient(srv.URL, func() (string, error) { return "ApiKey o:k", nil }, srv.Client())
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	started := map[string]Child{}
	release := map[string]chan struct{}{}
	d, err := New(Options{
		Client: client, HostID: testHost, Dirs: []Dir{{Path: real, Name: "proj", Source: SourceArg}}, Max: 1,
		HeartbeatEvery: 10 * time.Millisecond, PollWait: time.Second,
		Start: func(c Child) (int, func() error, error) {
			mu.Lock()
			defer mu.Unlock()
			started[c.SessionID] = c
			ch := make(chan struct{})
			release[c.SessionID] = ch
			return 999999, func() error { <-ch; return nil }, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	site.queue = []sessionsync.Dispatch{
		{ID: "d1", Kind: "start", Cwd: proj, Prompt: "fix it\x1b[31m", Mode: "plan"},
		{ID: "d2", Kind: "start", Cwd: proj, Prompt: "second"},               // over the cap
		{ID: "d3", Kind: "start", Cwd: filepath.Dir(proj), Prompt: "escape"}, // not offered
		{ID: "d4", Kind: "start", Cwd: proj, Prompt: "x", Mode: "yolo"},
		{ID: "d5", Kind: "launch"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = d.Run(ctx); close(done) }()
	for i := 0; i < 5; i++ {
		select {
		case <-site.delivered:
		case <-time.After(5 * time.Second):
			t.Fatal("acks did not arrive")
		}
	}

	site.mu.Lock()
	if site.host.RC == nil || len(site.host.RC.Dirs) != 1 || site.host.RC.MaxSessions != 1 {
		t.Errorf("advertised host = %+v", site.host)
	}
	a1 := site.acks["d1"]
	if a1[0] != sessionsync.DispatchStarted || a1[1] == "" {
		t.Errorf("d1 ack = %v", a1)
	}
	for _, id := range []string{"d2", "d3", "d4", "d5"} {
		if a := site.acks[id]; a[0] != sessionsync.DispatchRefused || a[2] == "" {
			t.Errorf("%s ack = %v, want refused with a reason", id, a)
		}
	}
	sid := a1[1]
	site.queue = []sessionsync.Dispatch{{ID: "s1", Kind: "stop", SessionID: sid}, {ID: "s2", Kind: "stop", SessionID: "nope"}}
	site.mu.Unlock()

	mu.Lock()
	c := started[sid]
	mu.Unlock()
	if c.Prompt != sessionsync.CleanPrompt("fix it\x1b[31m") || strings.Contains(c.Prompt, "\x1b") || c.Mode != "plan" || c.Cwd != real {
		t.Errorf("child = %+v", c)
	}
	if r, live := ReadRecord(); !live || len(r.Sessions) != 1 {
		t.Errorf("record = %+v live=%v", r, live)
	}

	for i := 0; i < 2; i++ {
		select {
		case <-site.delivered:
		case <-time.After(5 * time.Second):
			t.Fatal("stop acks did not arrive")
		}
	}
	site.mu.Lock()
	if a := site.acks["s1"]; a[0] != sessionsync.DispatchStopped {
		t.Errorf("stop ack = %v", a)
	}
	if a := site.acks["s2"]; a[0] != sessionsync.DispatchRefused {
		t.Errorf("unknown stop ack = %v", a)
	}
	site.mu.Unlock()

	mu.Lock()
	close(release[sid])
	mu.Unlock()
	cancel()
	<-done
	site.mu.Lock()
	defer site.mu.Unlock()
	if !site.offline {
		t.Error("daemon did not go offline on shutdown")
	}
	if _, live := ReadRecord(); live {
		t.Error("record still live after shutdown")
	}
}

type fakeRunner struct {
	mu      sync.Mutex
	prompts []string
	history []int
	block   chan struct{}
}

func (f *fakeRunner) RunInputObserved(ctx context.Context, history []run.Turn, in agent.TurnInput, emit func(agent.Event)) (run.Result, error) {
	f.mu.Lock()
	f.prompts = append(f.prompts, in.Prompt)
	f.history = append(f.history, len(history))
	block := f.block
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	emit(agent.Event{Kind: agent.EventTextKind, Text: "done: " + in.Prompt})
	return run.Result{Reply: "done: " + in.Prompt}, nil
}

type fakeMirror struct {
	mu      sync.Mutex
	prompts chan sessionsync.RemotePrompt
	acks    []string
}

func (m *fakeMirror) Nudge()                                   {}
func (m *fakeMirror) Prompts() <-chan sessionsync.RemotePrompt { return m.prompts }
func (m *fakeMirror) RemotePromptsEnabled() bool               { return true }
func (m *fakeMirror) Ack(id, status, reason, entryID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.acks = append(m.acks, id+":"+status)
}

func TestRunSessionQueuesWebPromptsAndIdlesOut(t *testing.T) {
	r := &fakeRunner{block: make(chan struct{})}
	m := &fakeMirror{prompts: make(chan sessionsync.RemotePrompt, 4)}
	done := make(chan error)
	go func() {
		done <- RunSession(context.Background(), SessionOptions{
			Agent: r, Log: turnlog.New(nil), Mirror: m, Dispatch: "d1", Prompt: "first", Idle: 200 * time.Millisecond,
		})
	}()
	// Arrives while the first turn runs: queued, then run in order.
	m.prompts <- sessionsync.RemotePrompt{ID: "p1", Content: "/second"}
	m.prompts <- sessionsync.RemotePrompt{ID: "p2", Content: " \t\n "}
	time.Sleep(50 * time.Millisecond)
	close(r.block)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session did not idle out")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if strings.Join(r.prompts, "|") != "first|/second" {
		t.Fatalf("prompts = %q", r.prompts)
	}
	if r.history[1] != 2 {
		t.Fatalf("second turn saw %d history turns, want 2", r.history[1])
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	want := "p1:queued|p2:queued|p1:accepted|p2:refused"
	if strings.Join(m.acks, "|") != want {
		t.Fatalf("acks = %v, want %s", m.acks, want)
	}
}

func TestRunSessionStopsOnCancel(t *testing.T) {
	r := &fakeRunner{}
	m := &fakeMirror{prompts: make(chan sessionsync.RemotePrompt)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() {
		done <- RunSession(ctx, SessionOptions{Agent: r, Log: turnlog.New(nil), Mirror: m, Prompt: "go", Idle: time.Hour})
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("session ignored the stop")
	}
}

func TestDaemonStartsWorkersAndAdvertisesInventory(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	proj := t.TempDir()
	real, _ := Normalize(proj)
	site := &fakeSite{acks: map[string][3]string{}, delivered: make(chan struct{}, 8)}
	srv := httptest.NewServer(site)
	defer srv.Close()
	client, err := sessionsync.NewClient(srv.URL, func() (string, error) { return "ApiKey o:k", nil }, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	inv := Inventory{
		MaxWorkers: 4,
		Profiles:   []sessionsync.RCProfile{{Name: "belai:builder", Lists: []string{"backlog"}, Labels: []string{"build"}}},
		Crews:      []sessionsync.RCCrew{{Name: "belai:delivery", Members: []sessionsync.RCMember{{Profile: "belai:builder", Replicas: 2}}}},
		Workers:    []sessionsync.RCWorker{{ID: "builder-1", Profile: "belai:builder", State: "idle"}},
	}
	var starts []WorkerStart
	d, err := New(Options{
		Client: client, HostID: testHost, Dirs: []Dir{{Path: real, Name: "proj", Source: SourceTrusted}},
		HeartbeatEvery: 10 * time.Millisecond, PollWait: time.Second, Exe: "/bin/belai",
		Inventory: func() Inventory { mu.Lock(); defer mu.Unlock(); return inv },
		StartWorkers: func(w WorkerStart) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			starts = append(starts, w)
			if w.Crew != "" {
				return "", startError("starting 2 would run 5 workers; agents.max_workers is 4")
			}
			return "builder-2  idle", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	site.queue = []sessionsync.Dispatch{
		{ID: "w1", Kind: "worker", Cwd: proj, Profile: "belai:builder"},
		{ID: "w2", Kind: "worker", Cwd: proj, Profile: "-trust-dir"},
		{ID: "w3", Kind: "worker", Cwd: proj, Profile: "belai:ghost"},
		{ID: "w4", Kind: "worker", Cwd: filepath.Dir(proj), Profile: "belai:builder"},
		{ID: "c1", Kind: "crew", Cwd: proj, Crew: "belai:delivery"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = d.Run(ctx); close(done) }()
	for i := 0; i < 5; i++ {
		select {
		case <-site.delivered:
		case <-time.After(5 * time.Second):
			t.Fatal("acks did not arrive")
		}
	}
	// A new profile on disk is advertised again on the next beat.
	mu.Lock()
	inv.Profiles = append(inv.Profiles, sessionsync.RCProfile{Name: "docs-writer", Lists: []string{"backlog"}})
	mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		site.mu.Lock()
		n := 0
		if site.host.RC != nil {
			n = len(site.host.RC.Profiles)
		}
		// The worker list rides on a separate heartbeat; wait for it too.
		beat := len(site.workers) > 0
		site.mu.Unlock()
		if n == 2 && beat {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("changed catalogue or worker heartbeat was not advertised")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	site.mu.Lock()
	defer site.mu.Unlock()
	if site.host.RC.MaxWorkers != 4 || len(site.host.RC.Crews) != 1 {
		t.Errorf("advertised rc = %+v", site.host.RC)
	}
	if len(site.workers) != 1 || site.workers[0].ID != "builder-1" {
		t.Errorf("heartbeat workers = %+v", site.workers)
	}
	if a := site.acks["w1"]; a[0] != sessionsync.DispatchStarted || a[2] != "builder-2 idle" {
		t.Errorf("w1 ack = %v", a)
	}
	for _, id := range []string{"w2", "w3", "w4", "c1"} {
		if a := site.acks[id]; a[0] != sessionsync.DispatchRefused || a[2] == "" {
			t.Errorf("%s ack = %v, want refused with a reason", id, a)
		}
	}
	if a := site.acks["c1"]; !strings.Contains(a[2], "max_workers") {
		t.Errorf("crew refusal lost the start error: %v", a)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(starts) != 2 || starts[0].Profile != "belai:builder" || starts[0].Cwd != real || starts[1].Crew != "belai:delivery" {
		t.Errorf("starts = %+v", starts)
	}
}

func TestMaxWorkersOverride(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	proj := t.TempDir()
	real, _ := Normalize(proj)
	client, err := sessionsync.NewClient("http://127.0.0.1:1", func() (string, error) { return "ApiKey o:k", nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got WorkerStart
	d, err := New(Options{
		Client: client, HostID: testHost, Dirs: []Dir{{Path: real, Name: "proj", Source: SourceTrusted}},
		MaxWorkers: 15,
		Inventory: func() Inventory {
			return Inventory{MaxWorkers: 4, Profiles: []sessionsync.RCProfile{{Name: "belai:builder"}}}
		},
		StartWorkers: func(w WorkerStart) (string, error) { got = w; return "ok", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := d.o.Inventory().MaxWorkers; n != 15 {
		t.Errorf("advertised max workers = %d, want 15 (from --max)", n)
	}
	if _, refused := d.startWorkers(sessionsync.Dispatch{Kind: "worker", Cwd: proj, Profile: "belai:builder"}); refused != "" {
		t.Fatalf("start refused: %s", refused)
	}
	if got.MaxWorkers != 15 {
		t.Errorf("start MaxWorkers = %d, want 15", got.MaxWorkers)
	}
}

// A pause or resume names a worker id and reaches only what the host's own
// check allows; anything else is refused with a reason.
func TestDaemonPausesAndResumesWorkers(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	site := &fakeSite{acks: map[string][3]string{}, delivered: make(chan struct{}, 8)}
	srv := httptest.NewServer(site)
	defer srv.Close()
	client, err := sessionsync.NewClient(srv.URL, func() (string, error) { return "ApiKey o:k", nil }, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var calls []string
	d, err := New(Options{
		Client: client, HostID: testHost, HeartbeatEvery: 10 * time.Millisecond, PollWait: time.Second, Exe: "/bin/belai",
		PauseWorker: func(id string, pause bool) error {
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, fmt.Sprintf("%s %v", id, pause))
			if id == "gone-1" {
				return errors.New("that worker is not running on this host")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	site.queue = []sessionsync.Dispatch{
		{ID: "p1", Kind: "pause", Worker: "builder-1a2b"},
		{ID: "p2", Kind: "resume", Worker: "builder-1a2b"},
		{ID: "p3", Kind: "pause", Worker: "gone-1"},
		{ID: "p4", Kind: "pause", Worker: "../etc"},
		{ID: "p5", Kind: "pause"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = d.Run(ctx); close(done) }()
	for i := 0; i < 5; i++ {
		select {
		case <-site.delivered:
		case <-time.After(5 * time.Second):
			t.Fatal("acks did not arrive")
		}
	}
	cancel()
	<-done

	site.mu.Lock()
	defer site.mu.Unlock()
	for _, id := range []string{"p1", "p2"} {
		if a := site.acks[id]; a[0] != sessionsync.DispatchStarted {
			t.Errorf("%s ack = %v, want started", id, a)
		}
	}
	for _, id := range []string{"p3", "p4", "p5"} {
		if a := site.acks[id]; a[0] != sessionsync.DispatchRefused || a[2] == "" {
			t.Errorf("%s ack = %v, want refused with a reason", id, a)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 3 || calls[0] != "builder-1a2b true" || calls[1] != "builder-1a2b false" || calls[2] != "gone-1 true" {
		t.Errorf("calls = %v (an unsafe or empty id must never reach the registry)", calls)
	}
}
