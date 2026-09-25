package forge

import (
	"context"
	"sync"
	"time"
)

// Cache holds the latest Snapshot for one directory so the TUI's git tab and
// the agent session share a single probe. Get never blocks on the network;
// RefreshAsync starts at most one probe at a time.
type Cache struct {
	Runner Runner   // nil uses ExecRunner
	Look   LookPath // nil uses exec.LookPath
	// Budget bounds one whole probe; zero means DefaultBudget.
	Budget time.Duration

	mu       sync.Mutex
	snap     Snapshot
	dir      string
	at       time.Time
	have     bool
	inFlight bool
}

// DefaultBudget bounds one background probe, every CLI call included.
const DefaultBudget = 20 * time.Second

// Get returns the cached snapshot for dir and when it was taken. ok is false
// when nothing has been probed for dir yet.
func (c *Cache) Get(dir string) (snap Snapshot, at time.Time, ok bool) {
	if c == nil {
		return Snapshot{}, time.Time{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.have || c.dir != dir {
		return Snapshot{}, time.Time{}, false
	}
	return c.snap, c.at, true
}

// Store records a snapshot probed elsewhere (the TUI panel's own probe).
func (c *Cache) Store(dir string, snap Snapshot, at time.Time) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.have && c.dir == dir && at.Before(c.at) {
		return
	}
	c.snap, c.dir, c.at, c.have = snap, dir, at, true
}

// RefreshAsync starts a background probe of dir unless one is running or the
// cached snapshot for dir is younger than maxAge. done, when non-nil, is
// called with the result on the probe goroutine.
func (c *Cache) RefreshAsync(dir string, maxAge time.Duration, done func(Snapshot)) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.inFlight || (c.have && c.dir == dir && time.Since(c.at) < maxAge) {
		c.mu.Unlock()
		return
	}
	c.inFlight = true
	r, look, budget := c.Runner, c.Look, c.Budget
	c.mu.Unlock()
	if r == nil {
		r = ExecRunner
	}
	if budget <= 0 {
		budget = DefaultBudget
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), budget)
		snap := Probe(ctx, r, look, dir)
		cancel()
		at := time.Now()
		c.mu.Lock()
		c.inFlight = false
		c.mu.Unlock()
		c.Store(dir, snap, at)
		if done != nil {
			done(snap)
		}
	}()
}
