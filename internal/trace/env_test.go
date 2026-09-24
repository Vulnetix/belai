package trace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnvUnsetDisabled(t *testing.T) {
	t.Setenv("SIGNET_TRACE", "")
	if w := Env(); w != nil {
		t.Fatalf("Env() = %v, want nil when SIGNET_TRACE is unset", w)
	}
}

func TestEnvWritesToNamedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "env-trace.jsonl")
	t.Setenv("SIGNET_TRACE", path)

	w := Env()
	if w == nil {
		t.Fatal("Env() returned nil for a valid SIGNET_TRACE path")
	}
	defer w.Close()
	w.Event("agent", "turn", time.Millisecond)
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var r Record
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &r); err != nil {
		t.Fatalf("trace line malformed: %v (%q)", err, raw)
	}
	if r.Phase != "agent" || r.Event != "turn" || r.Duration != "1ms" {
		t.Fatalf("record = %+v", r)
	}
}

func TestEnvUnopenablePathDisabled(t *testing.T) {
	// A path whose parent is a regular file cannot be opened; Env must swallow
	// the error and disable tracing rather than failing the session.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SIGNET_TRACE", filepath.Join(blocker, "trace.jsonl"))

	if w := Env(); w != nil {
		t.Fatalf("Env() = %v, want nil for an unopenable path", w)
	}
}

func TestRecordPreservesExplicitTimestamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ts.jsonl")
	w, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer w.Close()

	// A caller-supplied TS must not be overwritten with the current time.
	w.Record(Record{TS: "fixed", Phase: "agent", Event: "e"})
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var r Record
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if r.TS != "fixed" {
		t.Fatalf("TS = %q, want the caller-supplied value", r.TS)
	}
}

func TestRecordDefaultsTimestamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ts-default.jsonl")
	w, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer w.Close()

	w.Record(Record{Phase: "agent", Event: "e"})
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var r Record
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if r.TS == "" {
		t.Fatal("TS must be defaulted to a timestamp")
	}
	if _, err := time.Parse(time.RFC3339Nano, r.TS); err != nil {
		t.Fatalf("TS = %q is not RFC3339Nano: %v", r.TS, err)
	}
}

func TestOpenErrorWhenDirectoryUncreatable(t *testing.T) {
	// Open must surface a real error when it cannot create the parent
	// directory, unlike Env which best-efforts it away.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filepath.Join(blocker, "trace.jsonl")); err == nil {
		t.Fatal("Open() should fail when the parent is a regular file")
	}
}
