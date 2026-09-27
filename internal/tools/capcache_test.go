package tools

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestProbeCacheReusesVerdicts(t *testing.T) {
	c := &probeCache{entries: map[string]probeEntry{}, now: time.Now, path: filepath.Join(t.TempDir(), "p.json")}
	var runs atomic.Int32
	run := c.cachedRun(func(ctx context.Context, name string, args ...string) bool { runs.Add(1); return true })
	// "sh" is on every PATH the suite runs with; the probe itself is faked.
	if !run(context.Background(), "sh", "-c", "true") || !run(context.Background(), "sh", "-c", "true") {
		t.Fatal("verdict lost")
	}
	if runs.Load() != 1 {
		t.Fatalf("probe ran %d times, want 1", runs.Load())
	}
	c.save()
	if v, hit := c.get(probeKey2(t, "sh", "-c", "true")); !hit || !v {
		t.Fatal("saved verdict not reused")
	}

	// Expired but within the stale window: served at once, refreshed behind.
	c.mu.Lock()
	c.now = func() time.Time { return time.Now().Add(probeCacheTTL + time.Minute) }
	c.mu.Unlock()
	if !run(context.Background(), "sh", "-c", "true") {
		t.Fatal("stale verdict not served")
	}
	deadline := time.Now().Add(2 * time.Second)
	for runs.Load() != 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if runs.Load() != 2 {
		t.Fatalf("stale verdict was not refreshed in the background (runs=%d)", runs.Load())
	}

	// Past the stale window the probe runs before answering.
	c.mu.Lock()
	c.now = func() time.Time { return time.Now().Add(probeStaleTTL + time.Hour) }
	c.mu.Unlock()
	before := runs.Load()
	run(context.Background(), "sh", "-c", "true")
	if runs.Load() != before+1 {
		t.Fatal("a verdict past the stale window was served without probing")
	}
}

func TestProbeCacheSkipsTimedOutProbes(t *testing.T) {
	c := &probeCache{entries: map[string]probeEntry{}, now: time.Now}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	run := c.cachedRun(func(ctx context.Context, name string, args ...string) bool { return false })
	run(ctx, "sh", "-c", "true")
	if len(c.entries) != 0 {
		t.Fatal("a probe cut short by the deadline was recorded")
	}
}

func probeKey2(t *testing.T, bin string, args ...string) (string, int64) {
	t.Helper()
	k, mod, ok := probeKey(bin, args)
	if !ok {
		t.Skip("sh not on PATH")
	}
	return k, mod
}
