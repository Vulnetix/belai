package agentpool

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func h(id string, idx int) Handle {
	return Handle{ID: id, Label: id, Kind: "explore", Index: idx, Total: 3}
}

// TestAcquireFIFOOrder pins that waiters are admitted in arrival order, not
// completion order: with one slot, task 1 holds it, task 2 and task 3 queue;
// when task 1 releases, task 2 must be admitted before task 3.
func TestAcquireFIFOOrder(t *testing.T) {
	p := New(1)
	ctx := context.Background()

	l1, err := p.Acquire(ctx, h("e1", 0))
	if err != nil {
		t.Fatalf("acquire 1: %v", err)
	}

	waitQueued := func(id string) {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			for _, x := range p.Snapshot() {
				if x.ID == id && x.State == StateQueued {
					return
				}
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("%s never queued", id)
	}

	var order []string
	var mu sync.Mutex
	record := func(id string) {
		mu.Lock()
		order = append(order, id)
		mu.Unlock()
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		l, err := p.Acquire(ctx, h("e2", 1))
		if err != nil {
			t.Errorf("acquire 2: %v", err)
			return
		}
		record("e2")
		time.Sleep(20 * time.Millisecond)
		l.Done(StateDone, "")
	}()
	waitQueued("e2")

	go func() {
		defer wg.Done()
		l, err := p.Acquire(ctx, h("e3", 2))
		if err != nil {
			t.Errorf("acquire 3: %v", err)
			return
		}
		record("e3")
		l.Done(StateDone, "")
	}()
	waitQueued("e3")

	l1.Done(StateDone, "")
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "e2" || order[1] != "e3" {
		t.Fatalf("admission order = %v, want [e2 e3]", order)
	}
}

// TestAcquireContextCancelWhileQueued pins that esc drops queued work at once:
// a cancelled context returns promptly without a slot ever being granted.
func TestAcquireContextCancelWhileQueued(t *testing.T) {
	p := New(1)
	ctx, cancel := context.WithCancel(context.Background())

	l1, err := p.Acquire(ctx, h("e1", 0))
	if err != nil {
		t.Fatalf("acquire 1: %v", err)
	}

	acquired := make(chan struct{})
	go func() {
		if _, err := p.Acquire(ctx, h("e2", 1)); err == nil {
			close(acquired)
		}
	}()

	// e2 is queued behind e1. Cancelling the shared ctx must drop it.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-acquired:
		t.Fatal("queued acquire must not be granted after ctx cancel")
	case <-time.After(200 * time.Millisecond):
	}

	snap := p.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("snapshot len = %d, want 2", len(snap))
	}
	if snap[1].ID != "e2" || snap[1].State != StateCancelled {
		t.Fatalf("e2 = %+v, want cancelled", snap[1])
	}

	// The slot is still held; releasing it must not leak or panic.
	l1.Done(StateDone, "")
}

// TestCancelRunningLease pins that Cancel on a running handle cancels its
// derived context, and Done then flips it to cancelled.
func TestCancelRunningLease(t *testing.T) {
	p := New(1)
	l, err := p.Acquire(context.Background(), h("e1", 0))
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	ctxErr := make(chan error, 1)
	go func() {
		<-l.Context().Done()
		ctxErr <- l.Context().Err()
	}()

	if !p.Cancel("e1") {
		t.Fatal("Cancel on a running handle must report true")
	}
	select {
	case err := <-ctxErr:
		if err == nil {
			t.Fatal("derived context must be cancelled")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("derived context was not cancelled")
	}

	l.Done(StateDone, "")
	snap := p.Snapshot()
	if snap[0].State != StateCancelled {
		t.Fatalf("state = %q, want cancelled (Done forces it from the cancelled ctx)", snap[0].State)
	}
}

// TestCancelQueuedHandle pins that Cancel removes a queued handle and its
// Acquire returns a context error promptly.
func TestCancelQueuedHandle(t *testing.T) {
	p := New(1)
	if _, err := p.Acquire(context.Background(), h("e1", 0)); err != nil {
		t.Fatalf("acquire 1: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := p.Acquire(context.Background(), h("e2", 1))
		done <- err
	}()

	time.Sleep(20 * time.Millisecond)
	if !p.Cancel("e2") {
		t.Fatal("Cancel on a queued handle must report true")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled queued acquire must return an error")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("queued acquire did not return after Cancel")
	}

	if p.Cancel("e2") {
		t.Fatal("Cancel on a terminal handle must report false")
	}
}

// TestSnapshotStates pins that Snapshot reports queued, running and terminal
// states in insertion order.
func TestSnapshotStates(t *testing.T) {
	p := New(2)
	l1, _ := p.Acquire(context.Background(), h("e1", 0))
	l2, _ := p.Acquire(context.Background(), h("e2", 1))

	queued := make(chan struct{})
	go func() {
		<-queued
	}()
	_ = queued
	// e3 queues: both slots are held.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		l, err := p.Acquire(context.Background(), h("e3", 2))
		if err == nil {
			l.Done(StateDone, "")
		}
	}()
	time.Sleep(30 * time.Millisecond)

	snap := p.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("snapshot len = %d, want 3", len(snap))
	}
	if snap[0].State != StateRunning || snap[1].State != StateRunning || snap[2].State != StateQueued {
		t.Fatalf("states = %q/%q/%q, want running/running/queued", snap[0].State, snap[1].State, snap[2].State)
	}

	l1.Done(StateDone, "")
	wg.Wait()

	snap = p.Snapshot()
	if snap[0].State != StateDone {
		t.Fatalf("e1 = %q, want done", snap[0].State)
	}
	if snap[2].State != StateDone {
		t.Fatalf("e3 = %q, want done (admitted after e1 released)", snap[2].State)
	}
	l2.Done(StateDone, "")
}

// TestObserveFiresOnRosterChange pins the roster change hook: every mutation
// delivers a fresh snapshot.
func TestObserveFiresOnRosterChange(t *testing.T) {
	p := New(1)
	var mu sync.Mutex
	var states []State
	p.Observe(func(hs []Handle) {
		mu.Lock()
		for _, h := range hs {
			states = append(states, h.State)
		}
		mu.Unlock()
	})

	l1, _ := p.Acquire(context.Background(), h("e1", 0))
	l1.Done(StateDone, "")

	mu.Lock()
	defer mu.Unlock()
	// Immediate admission emits running, then done.
	if len(states) != 2 || states[0] != StateRunning || states[1] != StateDone {
		t.Fatalf("observer states = %v, want [running done]", states)
	}
}

// TestSetSizeGrowsAdmitsQueued pins that raising the ceiling admits queued
// waiters immediately.
func TestSetSizeGrowsAdmitsQueued(t *testing.T) {
	p := New(1)
	l1, _ := p.Acquire(context.Background(), h("e1", 0))

	admitted := make(chan struct{})
	go func() {
		l, err := p.Acquire(context.Background(), h("e2", 1))
		if err == nil {
			close(admitted)
			l.Done(StateDone, "")
		}
	}()
	time.Sleep(30 * time.Millisecond)

	p.SetSize(2)
	select {
	case <-admitted:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("raising the ceiling must admit the queued waiter")
	}
	l1.Done(StateDone, "")
}

func TestSize(t *testing.T) {
	if got := New(0).Size(); got != 1 {
		t.Fatalf("New(0).Size() = %d, want 1", got)
	}
	if got := New(3).Size(); got != 3 {
		t.Fatalf("New(3).Size() = %d, want 3", got)
	}

	p := New(1)
	p.SetSize(5)
	if got := p.Size(); got != 5 {
		t.Fatalf("Size after SetSize(5) = %d, want 5", got)
	}
	p.SetSize(0)
	if got := p.Size(); got != 1 {
		t.Fatalf("Size after SetSize(0) = %d, want 1", got)
	}
}

// TestNoGoroutineLeak pins that Acquire returns promptly on cancellation and
// that Done releases every slot so the pool drains cleanly under -race.
func TestNoGoroutineLeak(t *testing.T) {
	p := New(3)
	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	var admitted int32
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			l, err := p.Acquire(ctx, Handle{ID: string(rune('a' + i)), Label: "x", Kind: "explore", Index: i, Total: 10})
			if err != nil {
				return
			}
			atomic.AddInt32(&admitted, 1)
			time.Sleep(200 * time.Millisecond)
			l.Done(StateDone, "")
		}(i)
	}

	// Cancel while the first three still hold their slots; the rest must drop
	// while queued instead of being admitted.
	time.Sleep(40 * time.Millisecond)
	cancel()
	wg.Wait()

	if n := atomic.LoadInt32(&admitted); n != 3 {
		t.Fatalf("admitted %d, want exactly 3 (pool size)", n)
	}
	if len(p.active) != 0 {
		t.Fatalf("active slots leaked: %d", len(p.active))
	}
}
