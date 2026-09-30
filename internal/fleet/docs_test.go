package fleet

import (
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/docparity"
)

// docs/fleet.md is the contract for a worker's states and for pausing. These
// tests fail when a state is added or renamed without the page following.
func TestFleetDocNamesEveryWorkerState(t *testing.T) {
	doc := docparity.Read(t, "docs/fleet.md")
	for _, s := range []State{StateStarting, StateIdle, StateWorking, StatePaused, StateStopping, StateStopped, StateFailed} {
		if !strings.Contains(doc, "`"+string(s)+"`") {
			t.Errorf("docs/fleet.md does not name the %q state", s)
		}
	}
}

func TestFleetDocDescribesPausing(t *testing.T) {
	doc := docparity.Read(t, "docs/fleet.md")
	for _, want := range []string{
		"### Pausing a worker",
		"belai agent pause",
		"belai agent resume",
		"finishes the card it holds",
		"quiet window",
		"empty marker file",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/fleet.md lacks %q", want)
		}
	}
}

// Only a live state holds a max_workers slot, and a paused worker is live: the
// docs, the website and the API all count it.
func TestLiveStatesMatchTheDocs(t *testing.T) {
	live := map[State]bool{StateStarting: true, StateIdle: true, StateWorking: true, StatePaused: true, StateStopping: true}
	for _, s := range []State{StateStarting, StateIdle, StateWorking, StatePaused, StateStopping, StateStopped, StateFailed} {
		if s.Live() != live[s] {
			t.Errorf("%q Live() = %v, want %v", s, s.Live(), live[s])
		}
	}
}

// Removing a record removes its pause marker, so a new worker with a recycled
// id does not start paused.
func TestRemoveClearsThePauseMarker(t *testing.T) {
	_, reg := testEnv(t)
	rec := Record{ID: "builder-1a2b", Profile: "p", State: StateStopped}
	if err := reg.Save(rec); err != nil {
		t.Fatal(err)
	}
	if err := reg.SetPaused(rec.ID, true); err != nil || !reg.Paused(rec.ID) {
		t.Fatalf("pause: %v", err)
	}
	if err := reg.Remove(rec.ID); err != nil {
		t.Fatal(err)
	}
	if reg.Paused(rec.ID) {
		t.Fatal("the pause marker outlived its record")
	}
}

// The quiet window that ends an idle worker never ends a paused one, however
// long it waits.
func TestPausedWorkerIsNotEndedByTheQuietWindow(t *testing.T) {
	store, reg := testEnv(t)
	p := builderProfile()
	p.Kanban.Poll = "10ms"
	w := newWorker(t, store, reg, p, complete)
	w.Once = false
	if err := reg.SetPaused(w.Record.ID, true); err != nil {
		t.Fatal(err)
	}
	rec, took := runFor(t, w, 400*time.Millisecond)
	if rec.Reason != "stopped" || took < 350*time.Millisecond {
		t.Fatalf("a paused worker ended by itself after %s: %+v", took, rec)
	}
}
