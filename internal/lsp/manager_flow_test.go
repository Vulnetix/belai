package lsp

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestWarmCallsStart(t *testing.T) {
	called := make(chan string, 1)
	m := NewManager(Options{
		Servers: map[string]string{"go": "/test/gopls"},
		Start: func(ctx context.Context, argv []string, dir string) (Conn, error) {
			called <- strings.Join(argv, " ")
			return nil, context.DeadlineExceeded
		},
	})
	defer m.Close()
	m.Warm("go")
	select {
	case a := <-called:
		if a != "/test/gopls serve" {
			t.Fatalf("argv = %q, want /test/gopls serve", a)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start not called")
	}
}

func TestWarmSkipsUnknown(t *testing.T) {
	m := NewManager(Options{})
	defer m.Close()
	m.Warm("bogus")
	m.mu.Lock()
	n := len(m.entries)
	m.mu.Unlock()
	if n != 0 {
		t.Fatalf("expected no entries for unknown language id, got %d", n)
	}
}

func TestEvictIdleClosesConn(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	m := NewManager(Options{Now: func() time.Time { return now }})
	defer m.Close()
	idleConn := &recordConn{}
	freshConn := &recordConn{}
	m.mu.Lock()
	m.entries[entryKey{langID: "go", binary: "x", root: "/r"}] = &entry{
		lang:     langByID["go"],
		conn:     idleConn,
		lastUsed: now.Add(-defaultIdleEviction - time.Second),
	}
	m.entries[entryKey{langID: "go", binary: "y", root: "/r"}] = &entry{
		lang:     langByID["go"],
		conn:     freshConn,
		lastUsed: now,
	}
	m.mu.Unlock()

	m.evictIdle()

	m.mu.Lock()
	n := len(m.entries)
	m.mu.Unlock()
	if n != 1 {
		t.Fatalf("expected 1 entry after idle eviction, got %d", n)
	}
	if !idleConn.closed {
		t.Fatal("idle conn not closed")
	}
	if freshConn.closed {
		t.Fatal("fresh conn should not be closed")
	}
}

func TestEvictIfNeededLocked(t *testing.T) {
	m := NewManager(Options{MaxLive: 1})
	defer m.Close()
	conn := &recordConn{}
	m.mu.Lock()
	m.entries[entryKey{langID: "go", binary: "x", root: "/r"}] = &entry{conn: conn, lastUsed: time.Unix(1, 0)}
	m.evictIfNeededLocked()
	m.mu.Unlock()
	if len(m.entries) != 0 {
		t.Fatalf("expected eviction at MaxLive, got %d entries", len(m.entries))
	}
	if !conn.closed {
		t.Fatal("evicted conn not closed")
	}

	below := NewManager(Options{MaxLive: 2})
	defer below.Close()
	c2 := &recordConn{}
	below.mu.Lock()
	below.entries[entryKey{langID: "go", binary: "x", root: "/r"}] = &entry{conn: c2, lastUsed: time.Unix(1, 0)}
	below.evictIfNeededLocked()
	below.mu.Unlock()
	if len(below.entries) != 1 {
		t.Fatalf("expected no eviction below MaxLive, got %d entries", len(below.entries))
	}
	if c2.closed {
		t.Fatal("conn closed when below MaxLive")
	}
}

func TestDetectAllOverride(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// "go" is guaranteed present in a Go test environment; the override branch
	// resolves it via exec.LookPath without touching the shared probe.
	out := DetectAll(ctx, map[string]string{"go": "go"})
	if p, ok := out["go"]; !ok || p == "" {
		t.Fatalf("DetectAll override go = %q, %v", p, ok)
	}
}
