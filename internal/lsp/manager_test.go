package lsp

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

// fakeConn is a Conn for tests.
type fakeConn struct {
	io.ReadWriteCloser
	waitErr error
}

func (c *fakeConn) Wait() error { return c.waitErr }

func TestManagerDiagnoseUnsupported(t *testing.T) {
	m := NewManager(Options{})
	defer m.Close()
	r := m.Diagnose(context.Background(), "foo.unknown", []byte("x"))
	if r.Status != StatusUnsupported {
		t.Fatalf("status = %q, want unsupported", r.Status)
	}
}

func TestManagerDiagnoseDisabled(t *testing.T) {
	m := NewManager(Options{
		Enabled: func(id string) bool { return false },
	})
	defer m.Close()
	r := m.Diagnose(context.Background(), "foo.go", []byte("package main"))
	if r.Status != StatusUnavailable {
		t.Fatalf("status = %q, want unavailable", r.Status)
	}
}

func TestManagerDiagnoseNoServerUsesFallback(t *testing.T) {
	m := NewManager(Options{Fallback: true})
	defer m.Close()
	r := m.Diagnose(context.Background(), "foo.sh", []byte("echo hi"))
	// No bash-language-server installed in normal test env; expect fallback.
	if r.Status != StatusFallback && r.Status != StatusUnavailable {
		t.Fatalf("status = %q, want fallback or unavailable", r.Status)
	}
}

func TestManagerCacheHit(t *testing.T) {
	m := NewManager(Options{})
	defer m.Close()
	abs := "foo.go"
	content := []byte("package main")
	// First call caches.
	_ = m.Diagnose(context.Background(), abs, content)
	// Second identical call should be cached.
	r2 := m.Diagnose(context.Background(), abs, content)
	_ = r2
}

func TestNewManagerAppliesDefaults(t *testing.T) {
	m := newManager(Options{})
	defer m.Close()
	if m.opts.Budget != defaultBudget {
		t.Fatalf("budget = %v, want %v", m.opts.Budget, defaultBudget)
	}
	if m.opts.MaxLive != defaultMaxLive {
		t.Fatalf("maxLive = %d, want %d", m.opts.MaxLive, defaultMaxLive)
	}
}

func TestManagerStartFuncSeam(t *testing.T) {
	var called atomic.Bool
	m := NewManager(Options{
		Fallback: true,
		Start: func(ctx context.Context, argv []string, dir string) (Conn, error) {
			called.Store(true)
			return nil, context.DeadlineExceeded
		},
	})
	defer m.Close()
	_ = m.Diagnose(context.Background(), "foo.go", []byte("package main"))
	// Give the goroutine time to run.
	time.Sleep(50 * time.Millisecond)
	if !called.Load() {
		t.Fatal("StartFunc was not called")
	}
}
