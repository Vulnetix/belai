package rc

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/docparity"
	"github.com/vulnetix/belai/internal/fleet"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// The dispatch kinds the daemon handles are the contract with the website. Each
// is named in docs/remote-control.md, and an unknown one is refused, so a new
// kind must be added to both the daemon and the page.
func TestDispatchKindsAreDocumented(t *testing.T) {
	doc := docparity.Read(t, "docs/remote-control.md")
	for _, want := range []string{"`pause` or `resume`", "Pause and resume", "pause marker", "refuses an id that does not look like one"} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/remote-control.md lacks %q", want)
		}
	}
}

// An unknown kind is refused with a reason that tells the host to update, so a
// website ahead of the host never silently drops a request.
func TestUnknownDispatchKindIsRefused(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	site := &fakeSite{acks: map[string][3]string{}, delivered: make(chan struct{}, 4)}
	srv := httptest.NewServer(site)
	defer srv.Close()
	client, err := sessionsync.NewClient(srv.URL, func() (string, error) { return "ApiKey o:k", nil }, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(Options{Client: client, HostID: testHost, HeartbeatEvery: 10 * time.Millisecond, PollWait: time.Second, Exe: "/bin/belai"})
	if err != nil {
		t.Fatal(err)
	}
	site.queue = []sessionsync.Dispatch{{ID: "u1", Kind: "reboot"}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = d.Run(ctx); close(done) }()
	select {
	case <-site.delivered:
	case <-time.After(5 * time.Second):
		t.Fatal("no ack arrived")
	}
	cancel()
	<-done

	site.mu.Lock()
	defer site.mu.Unlock()
	if a := site.acks["u1"]; a[0] != sessionsync.DispatchRefused || !strings.Contains(a[2], "update Belai") {
		t.Fatalf("ack = %v", a)
	}
}

// TestRemoteControlPageStatesTheDefaultsAndCaps pins the numbers in
// docs/remote-control.md to the constants.
func TestRemoteControlPageStatesTheDefaultsAndCaps(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/remote-control.md")), " ")
	for _, want := range []string{
		"It ends after `--idle` (default 30 minutes)",
		"At most `--max` sessions (default 3) run at once",
		"`agents.max_workers` (default 4) applies",
		"stopped or failed in the last 15 minutes, newest first, at most 64",
		"the last 12 lines of the worker's own log",
		"clipped to 240 bytes",
		"all the tails together stay under 48 KiB",
		"Every 30 seconds the daemon fires each enabled schedule",
		"The drawing takes at most 100 seconds on the host",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/remote-control.md does not say %q", want)
		}
	}
	if DefaultIdle != 30*time.Minute || DefaultMax != 3 || config.DefaultMaxWorkers != 4 {
		t.Errorf("idle %v, max %d, workers %d disagree with the page", DefaultIdle, DefaultMax, config.DefaultMaxWorkers)
	}
	if RecentWorkers != 15*time.Minute || maxInvWorkers != 64 || DefaultScheduleEvery != 30*time.Second || avatarTimeout != 100*time.Second {
		t.Errorf("recent %v, workers %d, schedule %v, avatar %v disagree with the page", RecentWorkers, maxInvWorkers, DefaultScheduleEvery, avatarTimeout)
	}
	if sessionsync.RCWorkerLogLines != 12 || sessionsync.RCWorkerLogLine != 240 || sessionsync.RCWorkerLogBudget != 48<<10 {
		t.Errorf("log caps %d/%d/%d disagree with the page", sessionsync.RCWorkerLogLines, sessionsync.RCWorkerLogLine, sessionsync.RCWorkerLogBudget)
	}
}

// TestWorkerInventoryHonoursTheDocumentedWindowAndCaps drives reportWorkers: a
// stopped worker stays for 15 minutes and not a second longer, the list stops at
// 64, a log keeps its last 12 lines, and the tails share the 48 KiB budget.
func TestWorkerInventoryHonoursTheDocumentedWindowAndCaps(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	dir := t.TempDir()
	rec := func(id string, state fleet.State, stoppedAgo time.Duration) fleet.Record {
		r := fleet.Record{ID: id, Profile: "builder", State: state}
		if stoppedAgo > 0 {
			r.Stopped = now.Add(-stoppedAgo).UnixMilli()
		}
		return r
	}
	live := fleet.NewID("builder")
	fresh := fleet.NewID("builder")
	edge := fleet.NewID("builder")
	stale := fleet.NewID("builder")
	got := reportWorkers([]fleet.Record{
		rec(live, fleet.StateWorking, 0),
		rec(fresh, fleet.StateStopped, 14*time.Minute+59*time.Second),
		rec(edge, fleet.StateFailed, 15*time.Minute),
		rec(stale, fleet.StateStopped, 15*time.Minute+time.Second),
	}, dir, now)
	ids := map[string]bool{}
	for _, w := range got {
		ids[w.ID] = true
	}
	if !ids[live] || !ids[fresh] || !ids[edge] || ids[stale] {
		t.Errorf("window: kept %v; want live, 14:59 and exactly 15:00 kept, 15:01 dropped", ids)
	}

	// More than 64 live workers: only 64 are reported.
	var many []fleet.Record
	for i := 0; i < maxInvWorkers+10; i++ {
		many = append(many, rec(fleet.NewID("builder"), fleet.StateIdle, 0))
	}
	if n := len(reportWorkers(many, dir, now)); n != maxInvWorkers {
		t.Errorf("%d workers reported, want %d", n, maxInvWorkers)
	}

	// A log keeps its last 12 lines, each cut to 240 bytes.
	id := fleet.NewID("builder")
	var b strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&b, "line %02d %s\n", i, strings.Repeat("x", 400))
	}
	if err := os.WriteFile(filepath.Join(dir, id+".log"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	w := reportWorkers([]fleet.Record{rec(id, fleet.StateWorking, 0)}, dir, now)[0]
	if len(w.Log) != sessionsync.RCWorkerLogLines || !strings.HasPrefix(w.Log[0], "line 18 ") || !strings.HasPrefix(w.Log[11], "line 29 ") {
		t.Fatalf("log tail = %d lines starting %q", len(w.Log), w.Log[0])
	}
	for _, l := range w.Log {
		if len(l) > sessionsync.RCWorkerLogLine+len("…") {
			t.Errorf("a log line is %d bytes, over the cap plus its ellipsis", len(l))
		}
	}

	// The tails of all workers together stay within the budget.
	var wide []fleet.Record
	for i := 0; i < 40; i++ {
		wid := fleet.NewID("builder")
		if err := os.WriteFile(filepath.Join(dir, wid+".log"), []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
		wide = append(wide, rec(wid, fleet.StateWorking, 0))
	}
	total := 0
	for _, w := range reportWorkers(wide, dir, now) {
		for _, l := range w.Log {
			total += len(l)
		}
	}
	if total > sessionsync.RCWorkerLogBudget {
		t.Errorf("the tails total %d bytes, over the %d budget", total, sessionsync.RCWorkerLogBudget)
	}
}
