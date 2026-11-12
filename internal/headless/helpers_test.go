package headless

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/rolemanager"
)

// OpenA2A, NewSession and the settings branch of diagnostics are wiring over
// live services (an A2A endpoint, a tool registry with MCP servers) and are
// exercised end to end by the e2e headless tests, not here.

func TestFormatStructured(t *testing.T) {
	for f, want := range map[Format]bool{
		FormatText:       false,
		FormatJSON:       true,
		FormatStreamJSON: true,
		Format(""):       false,
		Format("yaml"):   false,
	} {
		if got := f.Structured(); got != want {
			t.Errorf("Format(%q).Structured() = %v, want %v", f, got, want)
		}
	}
}

func TestEmitterReportsItsFormat(t *testing.T) {
	for _, f := range []Format{FormatText, FormatJSON, FormatStreamJSON} {
		if got := NewEmitter(f, io.Discard, EmitterOptions{}).Format(); got != f {
			t.Errorf("NewEmitter(%q).Format() = %q", f, got)
		}
	}
}

func TestSchemaErrorString(t *testing.T) {
	err := &SchemaError{Pointer: "/items/0/name", Code: "type"}
	if got, want := err.Error(), "schema: type at /items/0/name"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

func TestDiagnosticsPrefersTheCallersGate(t *testing.T) {
	gate := rolemanager.DiagnosticsGate{Budget: 3 * time.Second, MaxRows: 4, MaxRunes: 50}
	// The caller's gate is returned as given; the registry is never consulted.
	if got := diagnostics(Params{Diagnostics: &gate}, nil); got != gate {
		t.Fatalf("diagnostics = %+v, want the caller's %+v", got, gate)
	}
}

func TestPullKanban(t *testing.T) {
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	t.Run("no board is a no-op", func(t *testing.T) {
		PullKanban(context.Background(), nil, syncSettings(true, true), t.TempDir())
	})
	t.Run("disabled settings leave the board alone", func(t *testing.T) {
		store := queuedBoard(t)
		workdir := t.TempDir()
		tokenCredential(t, workdir)
		PullKanban(context.Background(), store, syncSettings(false, true), workdir)
		PullKanban(context.Background(), store, syncSettings(true, false), workdir)
		if n := outboxLen(t, store); n != 1 {
			t.Fatalf("outbox holds %d items after a disabled pull, want 1", n)
		}
	})
	t.Run("no credential leaves the board alone", func(t *testing.T) {
		store := queuedBoard(t)
		PullKanban(context.Background(), store, syncSettings(true, true), t.TempDir())
		if n := outboxLen(t, store); n != 1 {
			t.Fatalf("outbox holds %d items with no client, want 1", n)
		}
	})
	t.Run("a failed best-effort sync keeps the queued item", func(t *testing.T) {
		store := queuedBoard(t)
		workdir := t.TempDir()
		tokenCredential(t, workdir)
		// The loopback client has no server behind it: the sync fails, is
		// swallowed within its timeout, and the item waits for the next one.
		start := time.Now()
		PullKanban(context.Background(), store, syncSettings(true, true), workdir)
		if elapsed := time.Since(start); elapsed > 15*time.Second {
			t.Fatalf("PullKanban took %v, past its own 10s bound", elapsed)
		}
		if n := outboxLen(t, store); n != 1 {
			t.Fatalf("outbox holds %d items after a failed sync, want 1", n)
		}
	})
}
