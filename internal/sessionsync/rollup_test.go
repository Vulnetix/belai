package sessionsync

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// rawLine is one JSONL line with a meta object.
func rawLine(id, typ string, ts int64, meta map[string]any) string {
	b, _ := json.Marshal(map[string]any{"id": id, "type": typ, "timestamp": ts, "meta": meta})
	return string(b) + "\n"
}

func sessionWithMeasures(id string) []string {
	return []string{
		rawLine(id+"-a", "assistant", 10, map[string]any{
			"provider": "anthropic", "model": "claude-sonnet-4-5", "prompt_tokens": 1000, "completion_tokens": 100,
			"cache_read_tokens": 600, "cache_write_tokens": 200, "cost_micro_usd": 3030,
		}),
		rawLine(id+"-t", "tool", 20, map[string]any{"tool_name": "Bash"}),
		rawLine(id+"-c", "check_run", 30, map[string]any{"kind": "test", "program": "go", "subcommand": "test", "exit_code": 0, "duration_ms": 5, "ok": true}),
		rawLine(id+"-s", "turn_state", 1000, map[string]any{"state": "ended", "started_at": 0, "duration_ms": 900}),
	}
}

// The end call carries the rollup of the session's file, on a Close and on a
// replaced session.
func TestEndCarriesTheRollup(t *testing.T) {
	fake := newFake()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	dir := t.TempDir()
	const second = "33333333-3333-4333-8333-333333333333"
	p1, p2 := filepath.Join(dir, "1.jsonl"), filepath.Join(dir, "2.jsonl")
	appendLines(t, p1, sessionWithMeasures("one")...)
	appendLines(t, p2, line("b", "user", "2"))

	s := startSyncer(t, srv, false)
	s.Activate(SessionInfo{ID: testSess, Path: p1})
	eventually(t, "first session", func() bool { return fake.count(testSess) == 4 })
	s.Activate(SessionInfo{ID: second, Path: p2})
	eventually(t, "first ended", func() bool { fake.mu.Lock(); defer fake.mu.Unlock(); return fake.ended[testSess] })
	eventually(t, "second session", func() bool { return fake.count(second) == 1 })
	s.Close(time.Second)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	var first struct {
		Reason string `json:"reason"`
		Rollup map[string]any
	}
	if err := json.Unmarshal([]byte(fake.endBodies[testSess]), &first); err != nil || first.Reason != EndSuperseded || first.Rollup == nil {
		t.Fatalf("replaced session's end = %q", fake.endBodies[testSess])
	}
	r := first.Rollup
	if r["active_ms"] != 900.0 || r["wait_ms"] != 0.0 || r["tool_calls"] != 1.0 {
		t.Fatalf("rollup = %v", r)
	}
	if cc, _ := r["check_counts"].(map[string]any); cc["test"] != 1.0 || cc["build"] != 0.0 {
		t.Fatalf("check_counts = %v", r["check_counts"])
	}
	models, _ := r["models"].([]any)
	if len(models) != 1 {
		t.Fatalf("models = %v", r["models"])
	}
	m := models[0].(map[string]any)
	if m["model"] != "claude-sonnet-4-5" || m["cache_read_tokens"] != 600.0 || m["cost_micro_usd"] != 3030.0 || m["priced"] != true {
		t.Fatalf("model = %v", m)
	}

	// The live session's end carries a rollup too, with its reason unset.
	var last EndInfo
	if err := json.Unmarshal([]byte(fake.endBodies[second]), &last); err != nil || last.Rollup == nil {
		t.Fatalf("live session's end = %q", fake.endBodies[second])
	}
	if last.Rollup.CheckCounts != nil {
		t.Fatalf("a transcript no measuring host wrote reports check_counts %+v, want null", last.Rollup.CheckCounts)
	}
}

// A file that cannot be read ends the session as an older host's does.
func TestEndWithoutAFileHasNoRollup(t *testing.T) {
	if rollupOf("") != nil || rollupOf(filepath.Join(t.TempDir(), "missing.jsonl")) != nil {
		t.Fatal("rollupOf invented a rollup")
	}
	b, _ := json.Marshal(EndInfo{Reason: EndCompleted})
	if string(b) != `{"reason":"completed"}` {
		t.Fatalf("an end without a rollup = %s", b)
	}
}
