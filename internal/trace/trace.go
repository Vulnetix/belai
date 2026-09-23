// Package trace implements the opt-in SIGNET_TRACE JSONL performance writer.
//
// When SIGNET_TRACE names a file, the agent and TUI emit one JSON object per
// timed event, e.g. {"phase":"agent","event":"tool_result_classify","duration":"12ms"}.
// The file is append-only and opened per writer; concurrent writers append
// whole lines because each record is one Encoder.Encode call. Tracing is
// best-effort: an unopenable path simply disables it, never fails a session.
package trace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Record is one JSONL trace event. Duration is a Go duration string so the
// file stays human-readable while remaining unambiguous.
type Record struct {
	TS       string `json:"ts,omitempty"`
	Phase    string `json:"phase"`
	Event    string `json:"event"`
	Duration string `json:"duration,omitempty"`
	Verdict  string `json:"verdict,omitempty"`
	Tool     string `json:"tool,omitempty"`
	Pass     int    `json:"pass,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Model    string `json:"model,omitempty"`
}

// Writer appends one JSON line per event to an opt-in trace file.
type Writer struct {
	mu  sync.Mutex
	enc *json.Encoder
	f   *os.File
}

// Open creates (or appends to) the file at path. An empty path yields
// (nil, nil): tracing is off.
func Open(path string) (*Writer, error) {
	if path == "" {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &Writer{f: f, enc: json.NewEncoder(f)}, nil
}

// Env returns the writer named by SIGNET_TRACE. It returns nil when the
// variable is unset or the file cannot be opened: tracing is best-effort.
func Env() *Writer {
	w, err := Open(os.Getenv("SIGNET_TRACE"))
	if err != nil {
		return nil
	}
	return w
}

// Record appends one record. It is a no-op on a nil writer.
func (w *Writer) Record(r Record) {
	if w == nil {
		return
	}
	if r.TS == "" {
		r.TS = time.Now().UTC().Format(time.RFC3339Nano)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return
	}
	_ = w.enc.Encode(r)
}

// Event appends one record. It is a no-op on a nil writer.
func (w *Writer) Event(phase, event string, d time.Duration) {
	w.Record(Record{Phase: phase, Event: event, Duration: d.String()})
}

// Close flushes and closes the file. It is a no-op on a nil writer.
func (w *Writer) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}
