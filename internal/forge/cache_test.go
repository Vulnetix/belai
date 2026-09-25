package forge

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseAheadBehind(t *testing.T) {
	cases := map[string][2]int{"3\t1": {3, 1}, "0 0": {0, 0}, "": {0, 0}, "x": {0, 0}}
	for in, want := range cases {
		a, b := parseAheadBehind(in)
		if a != want[0] || b != want[1] {
			t.Errorf("%q: got %d/%d, want %v", in, a, b, want)
		}
	}
}

func TestProbeAheadBehind(t *testing.T) {
	root := t.TempDir()
	f := newFake().
		on("git rev-parse --show-toplevel", root).
		on("git branch --show-current", "feat").
		on("git rev-parse --abbrev-ref --symbolic-full-name @{u}", "origin/feat").
		on("git rev-list --left-right --count HEAD...@{u}", "2\t5")
	s := Probe(context.Background(), f.run, missing, root)
	if s.Upstream != "origin/feat" || s.Ahead != 2 || s.Behind != 5 {
		t.Errorf("%+v", s)
	}
}

func TestCacheGetStoreAndDir(t *testing.T) {
	var c Cache
	if _, _, ok := c.Get("/a"); ok {
		t.Fatal("empty cache answered")
	}
	now := time.Now()
	c.Store("/a", Snapshot{Branch: "x"}, now)
	if s, at, ok := c.Get("/a"); !ok || s.Branch != "x" || !at.Equal(now) {
		t.Fatalf("get: %+v %v %v", s, at, ok)
	}
	if _, _, ok := c.Get("/b"); ok {
		t.Fatal("snapshot for /a answered /b")
	}
	c.Store("/a", Snapshot{Branch: "old"}, now.Add(-time.Minute))
	if s, _, _ := c.Get("/a"); s.Branch != "x" {
		t.Fatal("older snapshot replaced a newer one")
	}
	var nilCache *Cache
	if _, _, ok := nilCache.Get("/a"); ok {
		t.Fatal("nil cache answered")
	}
	nilCache.RefreshAsync("/a", 0, nil) // must not panic
}

func TestCacheRefreshAsyncSingleFlightAndFreshness(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	r := func(ctx context.Context, dir string, argv ...string) ([]byte, error) {
		if calls.Add(1) == 1 {
			<-release
		}
		return nil, context.Canceled // not a repo: Probe stops after one call
	}
	c := &Cache{Runner: r, Look: missing}
	done := make(chan Snapshot, 2)
	c.RefreshAsync("/a", time.Minute, func(s Snapshot) { done <- s })
	c.RefreshAsync("/a", time.Minute, func(s Snapshot) { done <- s }) // in flight: ignored
	close(release)
	<-done
	select {
	case <-done:
		t.Fatal("second refresh ran while the first was in flight")
	case <-time.After(50 * time.Millisecond):
	}
	if _, _, ok := c.Get("/a"); !ok {
		t.Fatal("probe result not stored")
	}
	c.RefreshAsync("/a", time.Minute, func(s Snapshot) { done <- s }) // fresh: ignored
	select {
	case <-done:
		t.Fatal("fresh snapshot re-probed")
	case <-time.After(50 * time.Millisecond):
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("runner called %d times", n)
	}
}
