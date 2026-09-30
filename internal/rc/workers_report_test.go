package rc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/fleet"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// The heartbeat carries every live worker, then the ones that ended within
// RecentWorkers (newest first) with their exit reason, and drops older ones.
func TestReportWorkersLiveThenRecent(t *testing.T) {
	now := time.Date(2026, 9, 29, 8, 0, 0, 0, time.UTC)
	ms := func(d time.Duration) int64 { return now.Add(-d).UnixMilli() }
	all := []fleet.Record{
		{ID: "belai-builder-000001", Profile: "belai:builder", State: fleet.StateStopped, Stopped: ms(20 * time.Minute), Reason: "old"},
		{ID: "belai-builder-000002", Profile: "belai:builder", State: fleet.StateStopped, Stopped: ms(10 * time.Minute), Reason: "done: nothing left to claim for 1m0s", Done: 2},
		{ID: "belai-scout-000003", Profile: "belai:scout", State: fleet.StateFailed, Stopped: ms(1 * time.Minute), Reason: "the worker process exited without stopping"},
		{ID: "belai-reviewer-000004", Profile: "belai:reviewer", Crew: "belai:delivery", State: fleet.StateIdle, Started: ms(time.Minute)},
	}
	got := reportWorkers(all, t.TempDir(), now)
	var ids []string
	for _, w := range got {
		ids = append(ids, w.ID)
	}
	want := "belai-reviewer-000004 belai-scout-000003 belai-builder-000002"
	if strings.Join(ids, " ") != want {
		t.Fatalf("reported %v, want %s", ids, want)
	}
	if got[1].State != "failed" || got[1].Reason != "the worker process exited without stopping" || got[2].Done != 2 {
		t.Fatalf("ended workers lost their facts: %+v", got[1:])
	}
}

// Each worker's log tail is its own file, read by id from the log dir,
// cleaned, the last RCWorkerLogLines lines, within the overall budget.
func TestReportWorkersLogTail(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("belai: agent: startup failed\n\x1b[31mred\x1b[0m line\n")
	for i := range 20 {
		fmt.Fprintf(&b, "2026-09-29T08:00:%02d+10:00 line %d\n", i, i)
	}
	id := "belai-builder-00000a"
	if err := os.WriteFile(filepath.Join(dir, id+".log"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	got := reportWorkers([]fleet.Record{{ID: id, Profile: "belai:builder", State: fleet.StateIdle, Log: "/etc/passwd"}}, dir, now)
	log := got[0].Log
	if len(log) != sessionsync.RCWorkerLogLines || !strings.HasSuffix(log[len(log)-1], "line 19") {
		t.Fatalf("tail %q", log)
	}

	// A short log keeps its startup lines, with ANSI stripped.
	short := "belai-builder-00000b"
	os.WriteFile(filepath.Join(dir, short+".log"), []byte("belai: agent: startup failed\n\x1b[31mred\x1b[0m line\n"), 0o600)
	got = reportWorkers([]fleet.Record{{ID: short, Profile: "belai:builder", State: fleet.StateFailed, Stopped: now.UnixMilli()}}, dir, now)
	if strings.Join(got[0].Log, "|") != "belai: agent: startup failed|red line" {
		t.Fatalf("short tail %q", got[0].Log)
	}

	// The budget spans every worker: later workers lose their tails first.
	long := strings.Repeat("x", sessionsync.RCWorkerLogLine) + "\n"
	var recs []fleet.Record
	for i := range 40 {
		id := fmt.Sprintf("belai-builder-%06d", 100+i)
		os.WriteFile(filepath.Join(dir, id+".log"), []byte(strings.Repeat(long, 20)), 0o600)
		recs = append(recs, fleet.Record{ID: id, Profile: "belai:builder", State: fleet.StateIdle})
	}
	total := 0
	for _, w := range reportWorkers(recs, dir, now) {
		for _, l := range w.Log {
			total += len(l)
		}
	}
	if total > sessionsync.RCWorkerLogBudget {
		t.Fatalf("log bytes %d over the budget %d", total, sessionsync.RCWorkerLogBudget)
	}
}

// Pausing reads the host's own registry: a live worker is paused and resumed,
// an unknown or ended one is refused.
func TestSetWorkerPausedOnlyReachesLiveWorkers(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	reg, err := fleet.OpenRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	live := fleet.Record{ID: "builder-1a2b", Profile: "belai:builder", PID: os.Getpid(), State: fleet.StateIdle, Started: time.Now().UnixMilli()}
	ended := fleet.Record{ID: "old-9", Profile: "belai:builder", PID: os.Getpid(), State: fleet.StateStopped, Started: 1, Stopped: 2}
	for _, r := range []fleet.Record{live, ended} {
		if err := reg.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := setWorkerPaused("builder-1a2b", true); err != nil || !reg.Paused("builder-1a2b") {
		t.Fatalf("pause: %v paused=%v", err, reg.Paused("builder-1a2b"))
	}
	if err := setWorkerPaused("builder-1a2b", false); err != nil || reg.Paused("builder-1a2b") {
		t.Fatalf("resume: %v paused=%v", err, reg.Paused("builder-1a2b"))
	}
	for _, id := range []string{"old-9", "ghost-1"} {
		if err := setWorkerPaused(id, true); err == nil || reg.Paused(id) {
			t.Fatalf("%s: paused an ended or unknown worker (%v)", id, err)
		}
	}
}
