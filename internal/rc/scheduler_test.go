package rc

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/schedule"
	"github.com/vulnetix/belai/internal/sessionsync"
)

const (
	schedA = "55555555-5555-4555-8555-555555555555"
	schedB = "66666666-6666-4666-8666-666666666666"
)

// fakeScheduleRemote is the website half of schedule sync.
type fakeScheduleRemote struct {
	mu      sync.Mutex
	changes []sessionsync.Schedule
	pushed  [][]sessionsync.Schedule
	err     error
}

func (f *fakeScheduleRemote) ScheduleList(_ context.Context, _ string, since int64, _ int) ([]sessionsync.Schedule, int64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, 0, false, f.err
	}
	var out []sessionsync.Schedule
	cursor := since
	for _, c := range f.changes {
		if c.Version > since {
			out = append(out, c)
			cursor = max(cursor, c.Version)
		}
	}
	return out, cursor, false, nil
}

func (f *fakeScheduleRemote) ScheduleBatch(_ context.Context, _ string, items []sessionsync.Schedule) ([]sessionsync.ScheduleAck, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	f.pushed = append(f.pushed, items)
	acks := make([]sessionsync.ScheduleAck, len(items))
	for i, it := range items {
		acks[i] = sessionsync.ScheduleAck{ID: it.ID, UpdatedAt: it.UpdatedAt, Version: 50, Applied: true}
	}
	return acks, nil
}

// schedHarness is a daemon with a fake clock, a fake worker start, a fake
// busy check and a fake website, and one offered directory.
type schedHarness struct {
	t      *testing.T
	d      *Daemon
	st     *schedule.Store
	now    time.Time
	remote *fakeScheduleRemote
	out    *bytes.Buffer
	dir    string

	mu       sync.Mutex
	starts   []WorkerStart
	startErr error
	busy     bool
}

func newSchedHarness(t *testing.T) *schedHarness {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	proj := t.TempDir()
	real, _ := Normalize(proj)
	client, err := sessionsync.NewClient("http://127.0.0.1:1", func() (string, error) { return "ApiKey o:k", nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := &schedHarness{
		t: t, dir: real, remote: &fakeScheduleRemote{}, out: &bytes.Buffer{},
		now: time.Date(2026, 10, 1, 9, 0, 0, 0, time.Local),
		st:  schedule.OpenAt(filepath.Join(t.TempDir(), "schedules.json")),
	}
	h.d, err = New(Options{
		Client: client, HostID: testHost, Exe: "/bin/belai", Out: h.out,
		Dirs: []Dir{{Path: real, Name: "proj", Source: SourceTrusted}},
		Inventory: func() Inventory {
			return Inventory{Profiles: []sessionsync.RCProfile{{Name: "builder"}, {Name: "belai:scout"}}}
		},
		Schedules:      h.st,
		ScheduleRemote: h.remote,
		Now:            func() time.Time { return h.now },
		Busy:           func(string, string) bool { h.mu.Lock(); defer h.mu.Unlock(); return h.busy },
		StartWorkers: func(w WorkerStart) (string, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.starts = append(h.starts, w)
			if h.startErr != nil {
				return "", h.startErr
			}
			return "builder-1  idle", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *schedHarness) add(id, cron string) schedule.Record {
	h.t.Helper()
	r, err := h.st.Add(schedule.Record{ID: id, Profile: "builder", Cron: cron, Dir: h.dir, Enabled: true}, h.now)
	if err != nil {
		h.t.Fatal(err)
	}
	return r
}

func (h *schedHarness) get(id string) schedule.Record {
	h.t.Helper()
	r, ok := h.st.Get(id)
	if !ok {
		h.t.Fatalf("schedule %s is missing", id)
	}
	return r
}

func (h *schedHarness) starting() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.starts)
}

func (h *schedHarness) at(hour, min, sec int) {
	h.now = time.Date(2026, 10, 1, hour, min, sec, 0, time.Local)
}

func TestScheduleFiresOnceWhenDue(t *testing.T) {
	h := newSchedHarness(t)
	h.add(schedA, "*/15 * * * *")

	// A new schedule gets its next run and fires nothing.
	h.d.fireDue(context.Background(), h.now)
	if h.starting() != 0 || h.get(schedA).NextRunAt != time.Date(2026, 10, 1, 9, 15, 0, 0, time.Local).UnixMilli() {
		t.Fatalf("new schedule: starts=%d %+v", h.starting(), h.get(schedA))
	}
	h.at(9, 14, 59)
	h.d.fireDue(context.Background(), h.now)
	if h.starting() != 0 {
		t.Fatal("fired before it was due")
	}
	h.at(9, 15, 10)
	h.d.fireDue(context.Background(), h.now)
	if h.starting() != 1 {
		t.Fatalf("starts = %d, want 1", h.starting())
	}
	w := h.starts[0]
	if w.Profile != "builder" || w.Cwd != h.dir || !w.Drain || w.Crew != "" || w.Exe != "/bin/belai" {
		t.Fatalf("worker start = %+v", w)
	}
	got := h.get(schedA)
	if got.LastStatus != schedule.StatusStarted || got.LastRunAt != h.now.UnixMilli() ||
		got.NextRunAt != time.Date(2026, 10, 1, 9, 30, 0, 0, time.Local).UnixMilli() {
		t.Fatalf("run record = %+v", got)
	}
	// The same tick, and every tick before the next, fire nothing more.
	h.d.fireDue(context.Background(), h.now)
	h.at(9, 29, 59)
	h.d.fireDue(context.Background(), h.now)
	if h.starting() != 1 {
		t.Fatalf("fired again within the same cron tick: %d", h.starting())
	}
	h.at(9, 30, 5)
	h.d.fireDue(context.Background(), h.now)
	if h.starting() != 2 {
		t.Fatalf("starts = %d, want 2 at the next tick", h.starting())
	}
}

func TestMissedRunsAreSkipped(t *testing.T) {
	h := newSchedHarness(t)
	h.add(schedA, "0 * * * *")
	if err := h.st.SetNext(schedA, time.Date(2026, 10, 1, 7, 0, 0, 0, time.Local)); err != nil {
		t.Fatal(err)
	}
	// The daemon starts at 9:00 with a run that was due at 7:00.
	h.d.rebaseSchedules()
	h.d.fireDue(context.Background(), h.now)
	if h.starting() != 0 {
		t.Fatal("a run missed while the daemon was down must not fire late")
	}
	if got := h.get(schedA); got.NextRunAt != time.Date(2026, 10, 1, 10, 0, 0, 0, time.Local).UnixMilli() {
		t.Fatalf("next run = %+v", got)
	}
	if !strings.Contains(h.out.String(), "skipped 1 run missed") {
		t.Fatalf("log:\n%s", h.out)
	}
}

func TestBusyAndCapAreRecordedAndNotRetriedUntilTheNextTick(t *testing.T) {
	h := newSchedHarness(t)
	h.add(schedA, "*/15 * * * *")
	h.d.fireDue(context.Background(), h.now)

	h.busy = true
	h.at(9, 15, 1)
	h.d.fireDue(context.Background(), h.now)
	if h.starting() != 0 || h.get(schedA).LastStatus != schedule.StatusSkippedBusy {
		t.Fatalf("busy: starts=%d %+v", h.starting(), h.get(schedA))
	}
	h.busy = false
	h.startErr = startError("starting 1 would run 4 workers; the worker cap is 3 (agents.max_workers, or --max-workers)")
	h.at(9, 30, 1)
	h.d.fireDue(context.Background(), h.now)
	if h.starting() != 1 || h.get(schedA).LastStatus != schedule.StatusRefusedCap {
		t.Fatalf("cap: starts=%d %+v", h.starting(), h.get(schedA))
	}
	// A refused run is not retried on the next 30 second tick.
	h.at(9, 30, 31)
	h.d.fireDue(context.Background(), h.now)
	if h.starting() != 1 {
		t.Fatalf("a refused start was retried within its cron tick: %d", h.starting())
	}
	h.startErr = nil
	h.at(9, 45, 1)
	h.d.fireDue(context.Background(), h.now)
	if got := h.get(schedA); h.starting() != 2 || got.LastStatus != schedule.StatusStarted {
		t.Fatalf("recovered: starts=%d %+v", h.starting(), got)
	}
}

func TestSchedulesTheHostCannotAcceptAreTurnedOff(t *testing.T) {
	h := newSchedHarness(t)
	cases := []struct {
		id, profile, cron, dir, status string
	}{
		{schedA, "builder", "61 * * * *", h.dir, schedule.StatusRefusedCron},
		{schedB, "builder", "* * * * *", filepath.Dir(h.dir), schedule.StatusRefusedDir},
		{"77777777-7777-4777-8777-777777777777", "ghost", "* * * * *", h.dir, schedule.StatusRefusedProfile},
		{"88888888-8888-4888-8888-888888888888", "--trust-dir", "* * * * *", h.dir, schedule.StatusRefusedProfile},
		{"99999999-9999-4999-8999-999999999999", "builder", "* * * * *", "", schedule.StatusRefusedDir},
	}
	for _, c := range cases {
		if _, err := h.st.Add(schedule.Record{ID: c.id, Profile: c.profile, Cron: c.cron, Dir: c.dir, Enabled: true}, h.now); err != nil {
			t.Fatal(err)
		}
	}
	h.d.fireDue(context.Background(), h.now)
	if h.starting() != 0 {
		t.Fatalf("an unacceptable schedule started a worker: %+v", h.starts)
	}
	for _, c := range cases {
		got := h.get(c.id)
		if got.Enabled || got.LastStatus != c.status || got.NextRunAt != 0 {
			t.Errorf("%s/%s: %+v, want off with %s", c.profile, c.cron, got, c.status)
		}
		if !got.Dirty {
			t.Errorf("%s: the refusal must be pushed so the website shows it", c.profile)
		}
	}
}

func TestDisabledScheduleNeverFiresAndReenablingSetsANextRun(t *testing.T) {
	h := newSchedHarness(t)
	h.add(schedA, "*/15 * * * *")
	h.d.fireDue(context.Background(), h.now)
	// The website turns it off, then on again.
	off := sessionsync.Schedule{ID: schedA, Profile: "builder", Cron: "*/15 * * * *", Dir: h.dir, Enabled: false,
		UpdatedAt: h.now.UnixMilli() + 1000, Version: 7}
	if _, err := h.st.Merge([]schedule.Record{schedule.FromWire(off)}, 7); err != nil {
		t.Fatal(err)
	}
	h.at(9, 20, 0)
	h.d.fireDue(context.Background(), h.now)
	if h.starting() != 0 {
		t.Fatal("a disabled schedule fired")
	}
	on := off
	on.Enabled, on.UpdatedAt, on.Version = true, off.UpdatedAt+1000, 8
	if _, err := h.st.Merge([]schedule.Record{schedule.FromWire(on)}, 8); err != nil {
		t.Fatal(err)
	}
	h.d.fireDue(context.Background(), h.now)
	if h.starting() != 0 || h.get(schedA).NextRunAt != time.Date(2026, 10, 1, 9, 30, 0, 0, time.Local).UnixMilli() {
		t.Fatalf("re-enabled: starts=%d %+v", h.starting(), h.get(schedA))
	}
}

func TestWebScheduleReachesTheHostAndTheRunRecordGoesBack(t *testing.T) {
	h := newSchedHarness(t)
	h.remote.changes = []sessionsync.Schedule{{
		ID: schedA, Profile: "belai:scout", Cron: "0 * * * *", Dir: h.dir, Enabled: true,
		CreatedAt: h.now.UnixMilli(), UpdatedAt: h.now.UnixMilli(), Version: 3,
	}}
	h.d.scheduleTick(context.Background())
	got := h.get(schedA)
	if got.Profile != "belai:scout" || got.NextRunAt != time.Date(2026, 10, 1, 10, 0, 0, 0, time.Local).UnixMilli() {
		t.Fatalf("pulled schedule: %+v", got)
	}
	if got.Dirty {
		t.Fatalf("the record was pushed in the same tick, so it must be clean: %+v", got)
	}
	if len(h.remote.pushed) != 1 || h.remote.pushed[0][0].NextRunAt == nil || h.remote.pushed[0][0].LastRunAt != nil {
		t.Fatalf("pushed = %+v: the next run goes back, no run has happened", h.remote.pushed)
	}
	// It fires at 10:00 and the run record goes back.
	h.at(10, 0, 5)
	h.d.scheduleTick(context.Background())
	if h.starting() != 1 {
		t.Fatalf("starts = %d", h.starting())
	}
	last := h.remote.pushed[len(h.remote.pushed)-1][0]
	if last.LastRunAt == nil || last.LastStatus != schedule.StatusStarted || last.Profile != "belai:scout" {
		t.Fatalf("last push = %+v", last)
	}
	// A web edit moves it: the next run is recomputed from the new cron.
	h.remote.changes = append(h.remote.changes, sessionsync.Schedule{
		ID: schedA, Profile: "belai:scout", Cron: "30 11 * * *", Dir: h.dir, Enabled: true,
		CreatedAt: h.now.UnixMilli(), UpdatedAt: h.now.UnixMilli() + 60_000, Version: 9,
	})
	h.d.scheduleTick(context.Background())
	if got := h.get(schedA); got.Cron != "30 11 * * *" || got.NextRunAt != time.Date(2026, 10, 1, 11, 30, 0, 0, time.Local).UnixMilli() {
		t.Fatalf("after the web edit: %+v", got)
	}
	if got := h.get(schedA); got.LastRunAt == 0 || got.LastStatus != schedule.StatusStarted {
		t.Fatalf("the web edit erased the run record: %+v", got)
	}
}

func TestAnUnreachableWebsiteNeverDelaysARunAndIsLoggedOnce(t *testing.T) {
	h := newSchedHarness(t)
	h.add(schedA, "*/15 * * * *")
	h.remote.err = errors.New("connection refused")
	h.d.scheduleTick(context.Background())
	h.at(9, 15, 5)
	h.d.scheduleTick(context.Background())
	h.at(9, 20, 0)
	h.d.scheduleTick(context.Background())
	if h.starting() != 1 {
		t.Fatalf("starts = %d: a schedule must fire with the website down", h.starting())
	}
	if n := strings.Count(h.out.String(), "pull: connection refused"); n != 1 {
		t.Fatalf("the failure was logged %d times:\n%s", n, h.out)
	}
	h.remote.err = nil
	h.d.scheduleTick(context.Background())
	if !strings.Contains(h.out.String(), "pull works again") {
		t.Fatalf("recovery not logged:\n%s", h.out)
	}
}

func TestStartRefusalsMapToStatuses(t *testing.T) {
	for reason, want := range map[string]string{
		"starting 1 would run 4 workers; the worker cap is 3":      schedule.StatusRefusedCap,
		"profile x: fleet workers are turned off (agents.enabled)": schedule.StatusRefusedDisabled,
		"this host does not offer that directory":                  schedule.StatusRefusedDir,
		"this host has no worker profile x":                        schedule.StatusRefusedProfile,
		"something else went wrong":                                schedule.StatusError,
	} {
		if got := startStatus(reason); got != want {
			t.Errorf("startStatus(%q) = %q, want %q", reason, got, want)
		}
	}
}

func TestSchedulerStopsWithItsContext(t *testing.T) {
	h := newSchedHarness(t)
	h.d.o.ScheduleEvery = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	h.d.wg.Add(1)
	go h.d.scheduler(ctx)
	time.Sleep(30 * time.Millisecond)
	cancel()
	done := make(chan struct{})
	go func() { h.d.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the scheduler outlived its context")
	}
}

func TestSameRepoMatchesTheRepositoryAndItsSubdirectories(t *testing.T) {
	repo := t.TempDir()
	sub := filepath.Join(repo, "pkg")
	if !sameRepo(repo, repo) || !sameRepo(repo, sub) {
		t.Fatal("a directory in the repository is the repository")
	}
	if sameRepo(repo, filepath.Dir(repo)) || sameRepo(repo, repo+"-other") || sameRepo("", repo) {
		t.Fatal("a sibling or parent is not the repository")
	}
}
