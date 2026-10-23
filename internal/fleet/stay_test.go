package fleet

import (
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStayIdleCapIsBounded(t *testing.T) {
	for _, c := range []struct{ in, want time.Duration }{
		{0, DefaultStayIdleMax},
		{-time.Minute, DefaultStayIdleMax},
		{time.Hour, time.Hour},
		{MaxStayIdleMax, MaxStayIdleMax},
		{MaxStayIdleMax + time.Second, MaxStayIdleMax},
		{1000 * time.Hour, MaxStayIdleMax},
	} {
		if got := StayIdleCap(c.in); got != c.want {
			t.Errorf("StayIdleCap(%s) = %s, want %s", c.in, got, c.want)
		}
	}
	if DefaultStayIdleMax != 6*time.Hour {
		t.Errorf("the documented default is 6h, got %s", DefaultStayIdleMax)
	}
}

// A staying worker waits for work, but only up to its idle cap: a stuck worker
// must not hold a max_workers slot forever.
func TestStayingWorkerExitsAtItsIdleCap(t *testing.T) {
	for name, c := range map[string]struct {
		cap  time.Duration
		want string
	}{
		"explicit": {2 * time.Hour, "2h0m0s"},
		"default":  {0, "6h0m0s"},
		"clamped":  {1000 * time.Hour, "24h0m0s"},
	} {
		t.Run(name, func(t *testing.T) {
			store, reg := testEnv(t)
			w := newWorker(t, store, reg, quickPoll(), complete)
			w.Once, w.Stay, w.StayIdleMax = false, true, c.cap
			// Every reading of the clock is an hour later than the last.
			var ticks atomic.Int64
			base := time.Now()
			w.now = func() time.Time { return base.Add(time.Duration(ticks.Add(1)) * time.Hour) }
			rec, took := runFor(t, w, 5*time.Second)
			if rec.State != StateStopped || !strings.HasPrefix(rec.Reason, "done: stay idle cap") || !strings.Contains(rec.Reason, c.want) {
				t.Fatalf("record %+v", rec)
			}
			if took >= 5*time.Second {
				t.Fatal("the worker waited for the context instead of exiting")
			}
		})
	}
}

// Within the cap a staying worker keeps waiting, as before.
func TestStayingWorkerWithinItsCapKeepsWaiting(t *testing.T) {
	store, reg := testEnv(t)
	w := newWorker(t, store, reg, quickPoll(), complete)
	w.Once, w.Stay, w.StayIdleMax = false, true, time.Hour
	rec, took := runFor(t, w, 300*time.Millisecond)
	if rec.Reason != "stopped" || took < 250*time.Millisecond {
		t.Fatalf("ended after %s: %+v", took, rec)
	}
}

// A spawned staying worker carries its idle cap, clamped, and a non-staying
// one never carries it.
func TestSpawnArgvCarriesTheIdleCapOnlyWithStay(t *testing.T) {
	got := spawnArgs("t-builder-aaaaaa", SpawnOptions{Profile: "builder", Stay: true, StayIdleMax: 1000 * time.Hour})
	if i := slices.Index(got, "-stay-idle-max"); i < 0 || got[i+1] != "24h0m0s" || !slices.Contains(got, "-stay") {
		t.Fatalf("argv = %v", got)
	}
	got = spawnArgs("t-builder-aaaaaa", SpawnOptions{Profile: "builder", StayIdleMax: time.Hour})
	if slices.Contains(got, "-stay") || slices.Contains(got, "-stay-idle-max") {
		t.Fatalf("argv = %v", got)
	}
}

// A staying crew run is bounded by its idle cap, not the quiet window, and
// agents.max_workers (the run's Max) still bounds what it starts.
func TestStayingRunEndsAtItsIdleCapAndStaysWithinMaxWorkers(t *testing.T) {
	store, reg := testEnv(t)
	repo := t.TempDir()
	run, _ := reg.LaunchCrew(CrewRun{Crew: "c", Scope: []ScopeDir{{Path: repo}}, Members: members("builder", 3), Stay: true, StayIdleMaxSecs: 7200}, nil)
	now := time.Now()
	demand := 0
	var spawned int
	var last SpawnOptions
	s := &Supervisor{Registry: reg, Store: store, RunID: run.ID, Max: 2, now: func() time.Time { return now },
		Demand: func(string, string) int { return demand },
		Spawn:  func(o SpawnOptions) (string, error) { spawned++; last = o; return "", errors.New("no") }}

	// Past the quiet window but inside the cap: a staying run keeps going.
	s.quiet = now.Add(-time.Hour)
	if why := s.Reconcile(run); why != "" {
		t.Fatalf("a staying run ended inside its cap: %q", why)
	}
	// Past the cap: it ends.
	s.quiet = now.Add(-3 * time.Hour)
	if why := s.Reconcile(run); !strings.HasPrefix(why, "done:") || !strings.Contains(why, "2h0m0s") {
		t.Fatalf("reason %q", why)
	}
	// No explicit cap means the default, so 5h of nothing is still inside it.
	run.StayIdleMaxSecs = 0
	s.quiet = now.Add(-5 * time.Hour)
	if why := s.Reconcile(run); why != "" {
		t.Fatalf("ended before the default cap: %q", why)
	}
	s.quiet = now.Add(-7 * time.Hour)
	if why := s.Reconcile(run); !strings.HasPrefix(why, "done:") {
		t.Fatalf("reason %q", why)
	}

	// Work arrives: the quiet clock resets, and the Max of 2 bounds the starts.
	demand, run.StayIdleMaxSecs = 5, 7200
	s.quiet = now.Add(-5 * time.Hour)
	spawned = 0
	if why := s.Reconcile(run); why != "" || spawned > 2 {
		t.Fatalf("reason %q, spawn attempts %d", why, spawned)
	}
	if !last.Stay || last.StayIdleMax != 2*time.Hour {
		t.Fatalf("a member worker did not carry the run's idle cap: %+v", last)
	}
	if !s.quiet.Equal(now) {
		t.Fatalf("work did not reset the idle clock: %s", s.quiet)
	}
}
