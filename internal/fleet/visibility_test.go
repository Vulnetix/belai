//go:build !windows

package fleet

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Spawn records the worker as starting with the child's pid, so a child that
// dies before it registers is swept to failed with its log, not lost.
func TestSpawnRecordsAStartingWorker(t *testing.T) {
	exe, err := exec.LookPath("false")
	if err != nil {
		t.Skip("no false binary")
	}
	_, reg := testEnv(t)
	id, err := reg.Spawn(SpawnOptions{Exe: exe, Repo: t.TempDir(), Profile: "belai:builder", Crew: "belai:delivery"})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := reg.Get(id)
	if err != nil || rec.PID <= 0 || rec.Crew != "belai:delivery" || !rec.Detached || rec.Log == "" {
		t.Fatalf("spawn record %+v %v", rec, err)
	}
	// No manual reaping: Spawn reaps its own child, even when, as here, the
	// caller lives on (the TUI's /fleet).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		all, _ := reg.List()
		for _, r := range all {
			if r.ID == id && r.State == StateFailed {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("a child that died before registering was never marked failed")
}

// The log says what the worker looks for, once that nothing matched, and
// why it stopped, so an idle worker explains itself.
func TestWorkerLogExplainsIdle(t *testing.T) {
	store, reg := testEnv(t)
	w := newWorker(t, store, reg, quickPoll(), complete)
	w.Once = false
	var log bytes.Buffer
	w.Log = &log
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	out := log.String()
	for _, want := range []string{
		"looking for backlog items labelled build unassigned or assigned to t-builder in any project",
		"exiting after 20ms with nothing to claim",
		"nothing to claim: no backlog items labelled build",
		"stopped: done: nothing left to claim",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("log lacks %q:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "nothing to claim: no"); n != 1 {
		t.Fatalf("the nothing-to-claim line repeated %d times:\n%s", n, out)
	}
}
