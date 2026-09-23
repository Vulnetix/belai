// Package agentpool caps every fan-out the harness launches — explore
// subagents and background agents — behind one settings-backed FIFO queue.
//
// It is deliberately small and dependency-free (standard library only). The
// Role Manager owns the instance and the queue order and reaches it through
// rolemanager.Pipeline, so queue admission is traced with the existing
// rolemanager.record helper rather than a new trace channel. A plain buffered
// channel semaphore would not be FIFO, so the queue is a container/list of
// waiters drained in arrival order by the release path.
package agentpool

import (
	"container/list"
	"context"
	"sync"
	"time"
)

// State is one subagent lifecycle state.
type State string

const (
	StateQueued    State = "queued"
	StateRunning   State = "running"
	StateDone      State = "done"
	StateCancelled State = "cancelled"
	StateFailed    State = "failed"
)

// Handle describes one pooled fan-out item. It is registered as queued the
// instant Acquire is called, before the item runs, so a queued subagent gets a
// roster chip before it is admitted.
type Handle struct {
	ID      string
	Label   string
	Kind    string
	State   State
	Index   int
	Total   int
	Started time.Time
	Outcome string
}

// Lease is one admitted slot. Done releases the slot and wakes the head of the
// queue, preserving FIFO arrival order.
type Lease struct {
	ctx     context.Context
	cancel  context.CancelFunc
	release func(State, string)
}

// Context returns the derived, cancellable context the lease owns. Cancel on
// the pool cancels it, so the holder must run its work against this context
// rather than the one it passed to Acquire.
func (l *Lease) Context() context.Context { return l.ctx }

// Done releases the slot. A cancelled derived context forces StateCancelled
// regardless of the state passed, so both esc (the parent context) and Cancel
// (one subagent) read as "cancelled".
func (l *Lease) Done(state State, outcome string) {
	if l.ctx.Err() != nil {
		state = StateCancelled
	}
	l.release(state, outcome)
}

// entry is the pool's bookkeeping for one handle. It is either queued (el is
// non-nil and it lives in the waiters list) or running (it lives in active).
type entry struct {
	handle   Handle
	cancel   context.CancelFunc
	ready    chan struct{}
	el       *list.Element // non-nil while queued
	admitted bool
	done     bool // Done already released this slot
}

// Pool caps how many fan-out items run at once and keeps the rest queued in
// FIFO arrival order.
type Pool struct {
	mu        sync.Mutex
	size      int
	active    map[string]*entry // running
	waiters   *list.List        // of *entry, FIFO
	byID      map[string]*entry
	all       []*entry // insertion order, for Snapshot
	observers []func([]Handle)
}

// New returns a Pool with at most size concurrent slots. A size below 1 is
// raised to 1.
func New(size int) *Pool {
	if size < 1 {
		size = 1
	}
	return &Pool{
		size:    size,
		active:  make(map[string]*entry),
		waiters: list.New(),
		byID:    make(map[string]*entry),
	}
}

// SetSize changes the concurrency ceiling. Shrinking never pre-empts a running
// slot; growing admits as many queued waiters as now fit.
func (p *Pool) SetSize(n int) {
	if n < 1 {
		n = 1
	}
	p.mu.Lock()
	p.size = n
	p.admitNextLocked()
	p.mu.Unlock()
	p.emit()
}

// Size returns the current concurrency ceiling.
func (p *Pool) Size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.size
}

// Acquire registers h as queued and blocks until a slot is free, returning a
// Lease. It selects on ctx.Done() while queued, so a parent cancellation drops
// queued work at once. The returned context is derived from ctx and is the
// context Cancel cancels for a running lease.
func (p *Pool) Acquire(ctx context.Context, h Handle) (*Lease, error) {
	ctx, cancel := context.WithCancel(ctx)
	e := &entry{handle: h, cancel: cancel, ready: make(chan struct{})}
	e.handle.State = StateQueued

	p.mu.Lock()
	p.byID[h.ID] = e
	p.all = append(p.all, e)
	if len(p.active) < p.size {
		p.admitLocked(e)
		p.mu.Unlock()
		p.emit()
		return &Lease{ctx: ctx, cancel: cancel, release: p.makeRelease(e)}, nil
	}
	e.el = p.waiters.PushBack(e)
	p.mu.Unlock()
	p.emit()

	select {
	case <-e.ready:
		// Admitted by the release path.
	case <-ctx.Done():
		p.mu.Lock()
		admitted := e.admitted
		if !admitted {
			e.handle.State = StateCancelled
			p.unlinkWaiterLocked(e)
		}
		p.mu.Unlock()
		p.emit()
		if !admitted {
			return nil, ctx.Err()
		}
		// Admitted concurrently with the cancellation: take the lease and let
		// the holder observe the cancelled context and release promptly.
	}
	return &Lease{ctx: ctx, cancel: cancel, release: p.makeRelease(e)}, nil
}

// Snapshot returns a copy of every handle in insertion order, with its current
// state. It never blocks.
func (p *Pool) Snapshot() []Handle {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.snapshotLocked()
}

// Cancel cancels one handle. A queued handle is removed from the queue and
// marked cancelled; a running handle has its derived context cancelled and
// flips to cancelled when the holder releases. Terminal handles are not
// affected. It reports whether a live handle was found.
func (p *Pool) Cancel(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.byID[id]
	if !ok {
		return false
	}
	switch e.handle.State {
	case StateQueued:
		e.handle.State = StateCancelled
		p.unlinkWaiterLocked(e)
		e.cancel()
	case StateRunning:
		e.cancel()
	default:
		return false
	}
	return true
}

// Observe registers a roster change hook. Every mutation of a handle's state
// delivers a fresh snapshot to the hook. The hook is invoked without the pool
// lock held, so it may call Snapshot or Cancel.
func (p *Pool) Observe(fn func([]Handle)) {
	if fn == nil {
		return
	}
	p.mu.Lock()
	p.observers = append(p.observers, fn)
	p.mu.Unlock()
}

// admitLocked flips an entry to running and, when the entry is queued, wakes
// its waiter. It must be called with the lock held.
func (p *Pool) admitLocked(e *entry) {
	e.handle.State = StateRunning
	e.handle.Started = time.Now()
	e.admitted = true
	p.active[e.handle.ID] = e
	close(e.ready)
}

// admitNextLocked drains the head of the queue into any free slots. It must be
// called with the lock held.
func (p *Pool) admitNextLocked() {
	for p.waiters.Len() > 0 && len(p.active) < p.size {
		el := p.waiters.Front()
		p.waiters.Remove(el)
		e := el.Value.(*entry)
		e.el = nil
		p.admitLocked(e)
	}
}

// unlinkWaiterLocked removes a queued entry from the waiters list. It must be
// called with the lock held and is a no-op for an admitted entry.
func (p *Pool) unlinkWaiterLocked(e *entry) {
	if e.el != nil {
		p.waiters.Remove(e.el)
		e.el = nil
	}
}

// makeRelease returns the lease's release closure.
func (p *Pool) makeRelease(e *entry) func(State, string) {
	return func(state State, outcome string) {
		p.mu.Lock()
		if e.done {
			p.mu.Unlock()
			return
		}
		e.done = true
		e.handle.State = state
		e.handle.Outcome = outcome
		delete(p.active, e.handle.ID)
		e.cancel()
		p.admitNextLocked()
		p.mu.Unlock()
		p.emit()
	}
}

// snapshotLocked copies the current handles. It must be called with the lock
// held.
func (p *Pool) snapshotLocked() []Handle {
	out := make([]Handle, 0, len(p.all))
	for _, e := range p.all {
		out = append(out, e.handle)
	}
	return out
}

// emit delivers a snapshot to every observer without the lock held.
func (p *Pool) emit() {
	p.mu.Lock()
	snap := p.snapshotLocked()
	obs := append([]func([]Handle){}, p.observers...)
	p.mu.Unlock()
	for _, o := range obs {
		o(snap)
	}
}
