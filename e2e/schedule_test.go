package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/rc"
	"github.com/vulnetix/belai/internal/schedule"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/trustgate"
)

const scheduleHost = "11111111-1111-4111-8111-111111111111"

// syncBuffer is a log the daemon's goroutines write and the test reads.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// scheduleSite is the rc half of the website: it takes the host, heartbeat and
// schedule sync calls, has no schedules of its own and never queues a request.
func scheduleSite() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/dispatch"):
			time.Sleep(50 * time.Millisecond)
			w.Write([]byte(`{"dispatches":[]}`))
		case strings.HasSuffix(r.URL.Path, "/schedules") && r.Method == http.MethodGet:
			w.Write([]byte(`{"items":[],"cursor":0,"more":false}`))
		case strings.HasSuffix(r.URL.Path, "/schedules"):
			var in struct {
				Items []sessionsync.Schedule `json:"items"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			acks := make([]sessionsync.ScheduleAck, len(in.Items))
			for i, it := range in.Items {
				acks[i] = sessionsync.ScheduleAck{ID: it.ID, UpdatedAt: it.UpdatedAt, Version: 1, Applied: true}
			}
			json.NewEncoder(w).Encode(map[string]any{"acks": acks})
		default:
			w.Write([]byte(`{"ok":true}`))
		}
	}))
}

// TestScheduledAgentRunsThroughTheDaemon drives the whole scheduled path with
// the real binary: a stored schedule comes due in a running rc daemon, which
// starts the profile with `belai agent start -drain`, the worker claims the
// board item and builds it on its own branch, and, with nothing left to claim,
// exits because the stored schedule is what started it.
func TestScheduledAgentRunsThroughTheDaemon(t *testing.T) {
	e := newFleetEnv(t)
	t.Setenv("BELAI_HOME", e.home)
	t.Setenv("BELAI_WORKTREES_DIR", e.wt)
	t.Setenv("BELAI_BASE_URL", e.url)
	t.Setenv("OPENAI_API_KEY", "test")
	// Nothing here may reach a real account: sync goes to the stub below only.
	site := scheduleSite()
	t.Cleanup(site.Close)
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")

	// A short poll keeps the drain wait (two polls) to ten seconds.
	md := filepath.Join(t.TempDir(), "t-builder.md")
	if err := os.WriteFile(md, []byte(strings.Replace(fleetBuilderMD, "  lease: 5m\n", "  lease: 5m\n  poll: 5s\n", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	e.mustBelai("agent", "import", md)
	if err := trustgate.Grant(e.repo, nil); err != nil {
		t.Fatal(err)
	}
	short := strings.TrimSpace(e.mustBelai("kanban", "add", "Add hello.txt", "-label", "build", "-priority", "2"))
	repo, err := rc.Normalize(e.repo)
	if err != nil {
		t.Fatal(err)
	}

	// A clock the test moves forward, so "every minute" comes due at once.
	var skew atomic.Int64
	now := func() time.Time { return time.Now().Add(time.Duration(skew.Load())) }

	store := schedule.OpenAt(e.home + "/schedules.json")
	rec, err := store.Add(schedule.Record{
		ID: "55555555-5555-4555-8555-555555555555", Profile: "t-builder",
		Cron: "* * * * *", Dir: repo, Enabled: true,
	}, now())
	if err != nil {
		t.Fatal(err)
	}

	client, err := sessionsync.NewClient(site.URL, func() (string, error) { return "ApiKey o:k", nil }, site.Client())
	if err != nil {
		t.Fatal(err)
	}
	var log syncBuffer
	d, err := rc.New(rc.Options{
		Exe: belaiBin, Client: client, HostID: scheduleHost, Out: &log, Max: 1,
		Dirs:           []rc.Dir{{Path: repo, Name: "repo", Source: rc.SourceTrusted}},
		Schedules:      store,
		ScheduleEvery:  50 * time.Millisecond,
		HeartbeatEvery: time.Second,
		PollWait:       time.Second,
		Now:            now,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	fail := func(format string, args ...any) {
		t.Helper()
		t.Fatalf(format+"\ndaemon log:\n%s", append(args, log.String())...)
	}

	// The first look gives a new schedule its next run and fires nothing.
	waitUntil(t, 10*time.Second, func() bool { r, _ := store.Get(rec.ID); return r.NextRunAt != 0 }, "next run set")
	if r, _ := store.Get(rec.ID); r.LastRunAt != 0 {
		fail("fired before it was due: %+v", r)
	}
	skew.Store(int64(2 * time.Minute))

	waitUntil(t, 90*time.Second, func() bool { r, _ := store.Get(rec.ID); return r.LastStatus != "" }, "run recorded")
	if r, _ := store.Get(rec.ID); r.LastStatus != schedule.StatusStarted {
		fail("run status = %q, want %q", r.LastStatus, schedule.StatusStarted)
	}

	// The worker the daemon started does the work: the item reaches review.
	waitUntil(t, 90*time.Second, func() bool { return e.item(short).List == "review" }, "item in review")

	// Drain: with nothing left to claim the worker exits instead of idling.
	waitUntil(t, 40*time.Second, func() bool {
		return strings.Contains(e.mustBelai("agent", "ps"), "no workers running")
	}, "worker exited")
}

func waitUntil(t *testing.T, limit time.Duration, ok func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

// TestBelaiRCCommandFiresAStoredSchedule runs the command itself, `belai rc`,
// as the user would: it must open the stored schedules, offer the directory,
// and start the worker when the cron comes due, with no test code in the
// daemon. It waits for a real minute boundary, so it takes up to a minute.
func TestBelaiRCCommandFiresAStoredSchedule(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for a cron minute")
	}
	e := newFleetEnv(t)
	site := scheduleSite()
	t.Cleanup(site.Close)
	t.Setenv("BELAI_HOME", e.home)
	t.Setenv("BELAI_WORKTREES_DIR", e.wt)
	t.Setenv("BELAI_BASE_URL", e.url)
	t.Setenv("OPENAI_API_KEY", "test")
	// The stub is the only website this run can reach, under a made-up login.
	t.Setenv("VULNETIX_WEB_URL", site.URL)
	t.Setenv("VULNETIX_API_KEY", "test-key")
	t.Setenv("VULNETIX_ORG_ID", "11111111-2222-4333-8444-555555555555")
	t.Setenv("VULNETIX_API_TOKEN", "")
	t.Setenv("HOME", t.TempDir())

	md := filepath.Join(t.TempDir(), "t-builder.md")
	if err := os.WriteFile(md, []byte(strings.Replace(fleetBuilderMD, "  lease: 5m\n", "  lease: 5m\n  poll: 5s\n", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	e.mustBelai("agent", "import", md)
	if err := trustgate.Grant(e.repo, nil); err != nil {
		t.Fatal(err)
	}
	short := strings.TrimSpace(e.mustBelai("kanban", "add", "Add hello.txt", "-label", "build", "-priority", "2"))
	repo, err := rc.Normalize(e.repo)
	if err != nil {
		t.Fatal(err)
	}
	store, err := schedule.Open()
	if err != nil {
		t.Fatal(err)
	}
	rec, err := store.Add(schedule.Record{
		ID: "77777777-7777-4777-8777-777777777777", Profile: "t-builder",
		Cron: "* * * * *", Dir: repo, Enabled: true,
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	var out syncBuffer
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, belaiBin, "rc", "--dir", repo)
	cmd.Dir = repo
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 20 * time.Second
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { cancel(); <-done })
	fail := func(format string, args ...any) {
		t.Helper()
		t.Fatalf(format+"\nbelai rc output:\n%s", append(args, out.String())...)
	}

	deadline := time.Now().Add(100 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			fail("belai rc exited early: %v", err)
		default:
		}
		if r, ok := store.Get(rec.ID); ok && r.LastStatus != "" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	r, _ := store.Get(rec.ID)
	if r.LastStatus != schedule.StatusStarted {
		fail("schedule record = %+v, want status %q", r, schedule.StatusStarted)
	}
	waitUntil(t, 60*time.Second, func() bool { return e.item(short).List == "review" }, "item in review")
	waitUntil(t, 40*time.Second, func() bool {
		return strings.Contains(e.mustBelai("agent", "ps"), "no workers running")
	}, "worker exited")
}
