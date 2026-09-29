package tui

import (
	"sync"

	"github.com/vulnetix/belai/internal/rolemanager"
)

// rmQueue carries role-manager activity from the recording goroutines to the
// render loop without ever dropping one. The feed used to hand activities over
// a 256-slot channel and discard on overflow, which was right for a render-only
// line but wrong for the session record: a burst of decisions under a busy
// screen lost rows from the transcript. The queue is unbounded and ordered;
// push never blocks, and pop waits for the next activity.
type rmQueue struct {
	mu     sync.Mutex
	items  []rolemanager.Activity
	wake   chan struct{}
	closed bool
}

func newRMQueue() *rmQueue { return &rmQueue{wake: make(chan struct{}, 1)} }

// push appends an activity. It never blocks and never drops.
func (q *rmQueue) push(a rolemanager.Activity) {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.items = append(q.items, a)
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// pop returns the oldest activity, waiting for one if the queue is empty. It
// returns false once the queue is closed and drained.
func (q *rmQueue) pop() (rolemanager.Activity, bool) {
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			a := q.items[0]
			q.items[0] = rolemanager.Activity{}
			q.items = q.items[1:]
			more := len(q.items) > 0
			q.mu.Unlock()
			if more {
				// Leave the wake token set for the next pop.
				select {
				case q.wake <- struct{}{}:
				default:
				}
			}
			return a, true
		}
		if q.closed {
			q.mu.Unlock()
			return rolemanager.Activity{}, false
		}
		q.mu.Unlock()
		<-q.wake
	}
}

// len is the number of queued activities.
func (q *rmQueue) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// close ends the queue: pop drains what is left and then reports false.
func (q *rmQueue) close() {
	q.mu.Lock()
	if !q.closed {
		q.closed = true
		close(q.wake)
	}
	q.mu.Unlock()
}
