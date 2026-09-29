package turnlog

import (
	"sync"

	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/session"
)

// roleSink writes role-manager decisions to a transcript from its own
// goroutine, in the order they were recorded. The rolemanager sink contract is
// that a sink never blocks the decision path, so activities queue here without
// bound and are never dropped; the goroutine drains the queue into the writer.
type roleSink struct {
	w *session.Writer

	mu     sync.Mutex
	wake   *sync.Cond
	queue  []rolemanager.Activity
	closed bool
	done   chan struct{}
}

func newRoleSink(w *session.Writer) *roleSink {
	s := &roleSink{w: w, done: make(chan struct{})}
	s.wake = sync.NewCond(&s.mu)
	return s
}

func (s *roleSink) push(a rolemanager.Activity) {
	s.mu.Lock()
	if !s.closed {
		s.queue = append(s.queue, a)
		s.wake.Signal()
	}
	s.mu.Unlock()
}

// run writes queued activities until the sink is closed and drained.
func (s *roleSink) run() {
	defer close(s.done)
	for {
		s.mu.Lock()
		for len(s.queue) == 0 && !s.closed {
			s.wake.Wait()
		}
		batch := s.queue
		s.queue = nil
		closed := s.closed
		s.mu.Unlock()
		for _, a := range batch {
			rec := a.Record()
			s.w.Entry(session.Entry{
				Type: rolemanager.RecordType, Role: rolemanager.RecordType,
				Content: rec.Content, Meta: rec.Meta, Timestamp: rec.Timestamp,
			})
		}
		if closed && len(batch) == 0 {
			return
		}
	}
}

func (s *roleSink) close() {
	s.mu.Lock()
	s.closed = true
	s.wake.Broadcast()
	s.mu.Unlock()
	<-s.done
}

// AttachRoleManager starts writing every role-manager decision made in this
// process to the transcript, whatever the display level, and returns a detach
// that stops the sink after writing what is queued. A Log with no writer
// attaches nothing.
//
// The sink is process-wide, so it belongs in a process that runs one session:
// a TUI, an rc session child, a fleet worker or a headless run each are one.
func (l *Log) AttachRoleManager() (detach func()) {
	if l == nil || l.w == nil {
		return func() {}
	}
	s := newRoleSink(l.w)
	go s.run()
	cancel := rolemanager.AddSink(s.push)
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			s.close()
		})
	}
}
