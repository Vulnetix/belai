package fleet

import (
	"strings"
	"testing"
	"time"
)

// A profile with a cron schedule keeps its worker waiting between ticks. A
// stored schedule fires the worker itself, so -drain lets it exit like any
// other worker once nothing is left to claim.
func TestDrainExitsACronWorkerWithNothingToClaim(t *testing.T) {
	store, reg := testEnv(t)
	p := quickPoll()
	p.Schedule = "0 0 1 1 *"

	waiting := newWorker(t, store, reg, p, complete)
	waiting.Once = false
	rec, took := runFor(t, waiting, 400*time.Millisecond)
	if rec.Reason != "stopped" || took < 350*time.Millisecond {
		t.Fatalf("a cron worker must stay until stopped: %s %+v", took, rec)
	}

	draining := newWorker(t, store, reg, p, complete)
	draining.Once, draining.Drain = false, true
	rec, took = runFor(t, draining, 5*time.Second)
	if !strings.HasPrefix(rec.Reason, "done: nothing left to claim") || took >= 5*time.Second {
		t.Fatalf("a draining cron worker must exit once the board is dry: %s %+v", took, rec)
	}
	if live, _ := reg.Live(); len(live) != 0 {
		t.Fatalf("still live: %+v", live)
	}
}
