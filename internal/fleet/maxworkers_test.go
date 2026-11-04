//go:build !windows

package fleet

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/config"
)

// A worker reserves its slot under the cap its start was checked against
// (belai rc --max), not agents.max_workers: with the default cap already
// taken, an rc --max start would otherwise fail in every child.
func TestWorkerReservesUnderStartCap(t *testing.T) {
	store, reg := testEnv(t)
	for i := range config.DefaultMaxWorkers {
		rec := Record{ID: fmt.Sprintf("busy-%d", i), Profile: "p", PID: os.Getpid(), State: StateIdle, Started: 1}
		if err := reg.Save(rec); err != nil {
			t.Fatal(err)
		}
	}
	w := newWorker(t, store, reg, builderProfile(), complete)
	if err := w.Run(context.Background()); !errors.Is(err, ErrFull) {
		t.Fatalf("default cap: %v, want ErrFull", err)
	}
	w = newWorker(t, store, reg, builderProfile(), complete)
	w.MaxWorkers = config.DefaultMaxWorkers + 1
	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("start cap: %v", err)
	}
}

// Spawn hands the start's cap to the child's argv.
func TestSpawnForwardsMaxWorkers(t *testing.T) {
	_, reg := testEnv(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "argv")
	exe := filepath.Join(dir, "fake-belai")
	script := "#!/bin/sh\necho \"$@\" > " + out + "\n"
	if err := os.WriteFile(exe, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Spawn(SpawnOptions{Exe: exe, Repo: dir, Profile: "belai:builder", MaxWorkers: 15}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(out); err == nil && len(b) > 0 {
			if !strings.Contains(string(b), "-max-workers 15 belai:builder") {
				t.Fatalf("argv %q", b)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("child never ran")
}

func TestSharedCap(t *testing.T) {
	cases := []struct{ max, reserved, want int }{
		{28, 0, 28},
		{28, 4, 24},
		{4, 9, 1}, // clamped: one slot stays open to every start
		{1, 1, 1},
		{0, 4, 0}, // no cap reserves nothing
		{8, -2, 8},
	}
	for _, c := range cases {
		if got := SharedCap(c.max, c.reserved); got != c.want {
			t.Errorf("SharedCap(%d, %d) = %d, want %d", c.max, c.reserved, got, c.want)
		}
	}
}

// reservedRun is a launched run of four builders over one directory, with a
// supervisor whose spawns are recorded and registered.
func reservedRun(t *testing.T, run CrewRun, demand int) (*Supervisor, CrewRun, *[]SpawnOptions) {
	t.Helper()
	store, reg := testEnv(t)
	run.Crew, run.Scope, run.Members = "c", []ScopeDir{{Path: t.TempDir()}}, members("builder", 4)
	launched, err := reg.LaunchCrew(run, nil)
	if err != nil {
		t.Fatal(err)
	}
	var spawned []SpawnOptions
	s := &Supervisor{Registry: reg, Store: store, RunID: launched.ID, Max: 4, MinTenure: time.Nanosecond,
		Demand: func(string, string) int { return demand },
		Spawn: func(o SpawnOptions) (string, error) {
			spawned = append(spawned, o)
			id := NewID(o.Profile)
			return id, reg.Save(Record{ID: id, Profile: o.Profile, Crew: o.Crew, Run: o.Run, Repo: o.Repo, PID: os.Getpid(), State: StateIdle, Started: time.Now().UnixMilli()})
		}}
	return s, launched, &spawned
}

// A run that is not the Software Factory's fills only the shared cap, and its
// members reserve their slots under it, so the reserved slots stay free.
func TestSupervisorLeavesReservedSlotsFree(t *testing.T) {
	s, run, spawned := reservedRun(t, CrewRun{ReservedWorkers: 2}, 5)
	if got, ok := s.Registry.LiveRun("c"); !ok || got.ReservedWorkers != 2 {
		t.Fatalf("run file lost reserved_workers: %+v", got)
	}
	s.Reconcile(run)
	if len(*spawned) != 2 {
		t.Fatalf("spawned %d with 2 of 4 reserved, want 2", len(*spawned))
	}
	for _, o := range *spawned {
		if o.MaxWorkers != 2 {
			t.Errorf("member reserves under %d, want the shared cap 2", o.MaxWorkers)
		}
	}
}

// A factory run with nothing waiting is held to the shared cap like any
// other: an idle staying member never sits in a reserved slot.
func TestSupervisorFactoryRunWithoutWorkStaysShared(t *testing.T) {
	s, run, spawned := reservedRun(t, CrewRun{ReservedWorkers: 2, Factory: true, Stay: true}, 0)
	s.Reconcile(run)
	if len(*spawned) != 2 || (*spawned)[0].MaxWorkers != 2 {
		t.Fatalf("spawned %+v, want 2 under the shared cap", *spawned)
	}
}

// A factory run with a card waiting may fill the reserved slots, up to the
// full cap.
func TestSupervisorFactoryRunFillsReservedSlotsForWaitingWork(t *testing.T) {
	s, run, spawned := reservedRun(t, CrewRun{ReservedWorkers: 2, Factory: true}, 3)
	s.Reconcile(run)
	if len(*spawned) != 4 {
		t.Fatalf("spawned %d, want the full cap 4", len(*spawned))
	}
	for _, o := range *spawned {
		if o.MaxWorkers != 4 {
			t.Errorf("member reserves under %d, want the full cap 4", o.MaxWorkers)
		}
	}
}
