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
