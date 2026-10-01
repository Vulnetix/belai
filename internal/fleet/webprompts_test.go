package fleet

import (
	"sync"
	"testing"

	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/turnlog"
)

type fakeMirror struct {
	mu   sync.Mutex
	acks map[string]string
}

func (f *fakeMirror) Prompts() <-chan sessionsync.RemotePrompt { return nil }
func (f *fakeMirror) Nudge()                                   {}
func (f *fakeMirror) Ack(id, status, _, _ string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acks[id] = status
}

type fakeSteer struct {
	got  []string
	full bool
}

func (s *fakeSteer) Steer(t string) bool {
	if s.full {
		return false
	}
	s.got = append(s.got, t)
	return true
}

func newInbox() (*webInbox, *fakeMirror) {
	m := &fakeMirror{acks: map[string]string{}}
	return &webInbox{mirror: m, facts: func() map[string]any { return map[string]any{"profile": "p"} }}, m
}

func TestWebInboxSteersRunningTurn(t *testing.T) {
	b, m := newInbox()
	s := &fakeSteer{}
	b.begin(s, &transcript{log: turnlog.New(nil)})
	b.deliver(sessionsync.RemotePrompt{ID: "a", Content: "  use the v2 API \x00 "})
	if len(s.got) != 1 || s.got[0] != "use the v2 API" {
		t.Fatalf("steered %q, want the cleaned message", s.got)
	}
	if m.acks["a"] != sessionsync.AckAccepted {
		t.Fatalf("ack = %q, want accepted", m.acks["a"])
	}
}

func TestWebInboxHoldsBetweenTurnsAndSteersNext(t *testing.T) {
	b, m := newInbox()
	b.deliver(sessionsync.RemotePrompt{ID: "a", Content: "first"})
	if m.acks["a"] != sessionsync.AckQueued {
		t.Fatalf("ack = %q, want queued", m.acks["a"])
	}
	s := &fakeSteer{}
	b.begin(s, &transcript{log: turnlog.New(nil)})
	if len(s.got) != 1 || s.got[0] != "first" || m.acks["a"] != sessionsync.AckAccepted {
		t.Fatalf("held message not steered into the next turn: %v %v", s.got, m.acks)
	}
	b.end()
	b.deliver(sessionsync.RemotePrompt{ID: "b", Content: "later"})
	if m.acks["b"] != sessionsync.AckQueued {
		t.Fatalf("after end, ack = %q, want queued", m.acks["b"])
	}
}

func TestWebInboxRefusals(t *testing.T) {
	b, m := newInbox()
	b.deliver(sessionsync.RemotePrompt{ID: "e", Content: " \x00 "})
	if m.acks["e"] != sessionsync.AckRefused {
		t.Fatalf("empty: ack = %q", m.acks["e"])
	}
	for i := 0; i < maxWebQueue; i++ {
		b.deliver(sessionsync.RemotePrompt{ID: string(rune('a' + i)), Content: "x"})
	}
	b.deliver(sessionsync.RemotePrompt{ID: "over", Content: "x"})
	if m.acks["over"] != sessionsync.AckRefused {
		t.Fatalf("full queue: ack = %q", m.acks["over"])
	}
	b2, m2 := newInbox()
	b2.begin(&fakeSteer{full: true}, &transcript{log: turnlog.New(nil)})
	b2.deliver(sessionsync.RemotePrompt{ID: "f", Content: "x"})
	if m2.acks["f"] != sessionsync.AckRefused {
		t.Fatalf("steer full: ack = %q", m2.acks["f"])
	}
}
