package trace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOpenEmptyPathDisabled(t *testing.T) {
	w, err := Open("")
	if err != nil {
		t.Fatalf("Open(\"\") = %v", err)
	}
	if w != nil {
		t.Fatalf("Open(\"\") = %v, want nil", w)
	}
	// Nil writer events and Close are no-ops.
	w.Event("agent", "turn", time.Second)
	if err := w.Close(); err != nil {
		t.Fatalf("nil Close = %v", err)
	}
}

func TestEventAppendsOneJSONLRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	w, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer w.Close()

	w.Event("agent", "pre_prompt", 3*time.Millisecond)
	w.Event("tui", "render", 1500*time.Microsecond)
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2:\n%s", len(lines), raw)
	}

	var first Record
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("unmarshal line 0: %v", err)
	}
	if first.Phase != "agent" || first.Event != "pre_prompt" || first.Duration != "3ms" {
		t.Fatalf("line 0 = %+v", first)
	}

	var second Record
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatalf("unmarshal line 1: %v", err)
	}
	if second.Phase != "tui" || second.Event != "render" || second.Duration != "1.5ms" {
		t.Fatalf("line 1 = %+v", second)
	}
}

func TestOpenAppendsExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	w, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	w.Event("agent", "one", time.Millisecond)
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	w2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	w2.Event("agent", "two", time.Millisecond)
	if err := w2.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if n := strings.Count(string(raw), "\n"); n != 2 {
		t.Fatalf("got %d lines, want 2:\n%s", n, raw)
	}
}

func TestConcurrentEventsKeepLinesWhole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	w, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer w.Close()

	const n = 200
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.Event("agent", "tool_result_classify", time.Millisecond)
		}()
	}
	wg.Wait()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != n {
		t.Fatalf("got %d lines, want %d", len(lines), n)
	}
	for i, line := range lines {
		var r Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("line %d malformed: %v (%q)", i, err, line)
		}
	}
}
