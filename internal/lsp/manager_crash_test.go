package lsp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// brokenServer seeds m with a live entry for abs whose transport is closed, so
// the next diagnosis fails with a server error rather than a timeout.
func brokenServer(t *testing.T, m *Manager, abs, root, binary string) *entry {
	t.Helper()
	lang := LanguageFor(abs)
	conn := &recordConn{}
	c := newClient(lang, conn, []string{root})
	c.t = newTransport(conn)
	c.t.mu.Lock()
	c.t.closed = true
	c.t.mu.Unlock()
	e := &entry{lang: lang, binary: binary, root: root, client: c, conn: conn, ready: true, budget: m.opts.Budget}
	m.mu.Lock()
	m.entries[entryKey{langID: lang.ID, binary: binary, root: root}] = e
	m.mu.Unlock()
	return e
}

func diagnoseWithin(t *testing.T, m *Manager, abs string) Report {
	t.Helper()
	done := make(chan Report, 1)
	go func() { done <- m.Diagnose(context.Background(), abs, []byte("package p\n")) }()
	select {
	case r := <-done:
		return r
	case <-time.After(3 * time.Second):
		t.Fatal("Diagnose did not return after a server error: the manager deadlocked")
		return Report{}
	}
}

func crashFixture(t *testing.T) (*Manager, string, string) {
	t.Helper()
	root := t.TempDir()
	abs := filepath.Join(root, "a.go")
	if err := os.WriteFile(abs, []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newManager(Options{Roots: []string{root}, Servers: map[string]string{"go": "/usr/bin/gopls"}, Budget: 200 * time.Millisecond})
	t.Cleanup(func() { _ = m.Close() })
	return m, abs, root
}

// A server that answers with an error is a crash, not a hang: the call returns
// and the report says the language is unavailable.
func TestAServerErrorReturnsInsteadOfDeadlocking(t *testing.T) {
	m, abs, root := crashFixture(t)
	brokenServer(t, m, abs, root, "/usr/bin/gopls")
	r := diagnoseWithin(t, m, abs)
	if r.Status != StatusUnavailable {
		t.Fatalf("status = %q, want %q", r.Status, StatusUnavailable)
	}
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// waitStarts waits for the background initialisation to have called Start n times.
func waitStarts(t *testing.T, starts *int32, n int32) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for atomic.LoadInt32(starts) < n {
		if time.Now().After(deadline) {
			t.Fatalf("Start called %d times, want %d", atomic.LoadInt32(starts), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // let the failed start record its crash
}

// The first crash allows one restart after the cooldown; a second leaves the
// server down, and neither is retried while down.
func TestCrashPolicyIsOneRestartAfterTheCooldown(t *testing.T) {
	root := t.TempDir()
	abs := filepath.Join(root, "a.go")
	if err := os.WriteFile(abs, []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var starts int32
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	m := newManager(Options{
		Roots: []string{root}, Servers: map[string]string{"go": "/usr/bin/gopls"}, Now: clock.now,
		Start: func(context.Context, []string, string) (Conn, error) {
			atomic.AddInt32(&starts, 1)
			return nil, errors.New("exec failed")
		},
	})
	t.Cleanup(func() { _ = m.Close() })

	// Cold start fails: first crash.
	if r := diagnoseWithin(t, m, abs); r.Status != StatusWarming {
		t.Fatalf("first call status = %q, want warming", r.Status)
	}
	waitStarts(t, &starts, 1)

	// Inside the cooldown nothing is started.
	clock.advance(restartCooldown - time.Second)
	if r := diagnoseWithin(t, m, abs); r.Status != StatusUnavailable {
		t.Fatalf("status inside the cooldown = %q, want unavailable", r.Status)
	}
	time.Sleep(50 * time.Millisecond)
	if n := atomic.LoadInt32(&starts); n != 1 {
		t.Fatalf("Start called %d times inside the cooldown, want 1", n)
	}

	// After the cooldown one restart is attempted; it fails too.
	clock.advance(2 * time.Second)
	diagnoseWithin(t, m, abs)
	waitStarts(t, &starts, 2)

	// Second crash: never started again, however long the wait.
	clock.advance(24 * time.Hour)
	if r := diagnoseWithin(t, m, abs); r.Status != StatusUnavailable {
		t.Fatalf("status after the second crash = %q, want unavailable", r.Status)
	}
	time.Sleep(50 * time.Millisecond)
	if n := atomic.LoadInt32(&starts); n != 2 {
		t.Fatalf("Start called %d times after the second crash, want 2", n)
	}
}

// Five timeouts in a row stop live diagnosis; a success would have cleared them.
func TestFiveStrikesStopAskingTheServer(t *testing.T) {
	m := newManager(Options{Budget: 800 * time.Millisecond})
	t.Cleanup(func() { _ = m.Close() })
	e := &entry{budget: 800 * time.Millisecond}
	for i := 1; i <= strikeDisable; i++ {
		e.strikes = i
		m.adjustStrike(e)
		_, live := m.liveBudget(e)
		if want := i < strikeDisable; live != want {
			t.Fatalf("after %d strikes live = %v, want %v", i, live, want)
		}
	}
	e.strikes = 0
	if b, live := m.liveBudget(e); !live || b <= 0 {
		t.Fatalf("a cleared entry must be live with a budget, got %v %v", b, live)
	}
}
