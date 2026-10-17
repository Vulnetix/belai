package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// poolMock is fleetMock behind a switch per model: a request for a model
// marked down gets a 503, as a provider outage does; the rest pass through.
type poolMock struct {
	mu   sync.Mutex
	down map[string]bool
}

func (m *poolMock) set(model string, down bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.down[model] = down
}

func newPoolMock(t *testing.T) (*poolMock, *httptest.Server) {
	t.Helper()
	inner := fleetMock(t)
	t.Cleanup(inner.Close)
	m := &poolMock{down: map[string]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &req)
		m.mu.Lock()
		down := m.down[req.Model]
		m.mu.Unlock()
		if down {
			http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
			return
		}
		resp, err := http.Post(inner.URL+r.URL.Path, r.Header.Get("Content-Type"), bytes.NewReader(body))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(srv.Close)
	return m, srv
}

const poolBuilderMD = `---
name: t-poolbuilder
description: Writes hello.txt for items labelled build
mode: worker
autonomy: autonomous
tools: [Read, Write, update_plan]
max_iterations: 2
kanban:
  labels: [build]
  on_success: {list: review, labels: [needs-review], drop_labels: [build]}
  lease: 5m
  poll: 5s
workspace: {isolation: worktree}
budget: {max_passes_per_item: 4}
---
WRITER-PERSONA. Implement the attached item.
`

type poolRun struct {
	ID         string `json:"id"`
	State      string `json:"state"`
	Reason     string `json:"reason"`
	Supervisor int    `json:"supervisor"`
	PoolPhase  string `json:"pool_phase"`
	PoolEpoch  int    `json:"pool_epoch"`
}

type poolWorker struct {
	ID       string `json:"id"`
	Run      string `json:"run"`
	State    string `json:"state"`
	Model    string `json:"model"`
	Done     int    `json:"done"`
	Sleeping bool   `json:"sleeping"`
}

// TestACrewRunsOnItsProviderPool drives a crew run's pool through the
// binary: a worker falls back past a model that is down, takes a model a
// person pins and hands it back, survives its supervisor being killed, sleeps
// when the whole pool is down and recovers when a model comes back, and the
// run ends with a reason when the pool fails again after the final retry.
func TestACrewRunsOnItsProviderPool(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a crew for about a minute")
	}
	e := newFleetEnv(t)
	mock, srv := newPoolMock(t)
	e.url = srv.URL
	os.WriteFile(filepath.Join(e.home, "settings.json"), []byte(`{"resilience":{"max_iterations":2,"max_attempts":1}}`), 0o600)
	md := filepath.Join(t.TempDir(), "t-poolbuilder.md")
	os.WriteFile(md, []byte(poolBuilderMD), 0o600)
	e.mustBelai("agent", "import", md)
	crew := filepath.Join(t.TempDir(), "t-pool.json")
	os.WriteFile(crew, []byte(`{"name":"t-pool","description":"one builder on a pool","members":[{"profile":"t-poolbuilder","replicas":1}]}`), 0o600)
	e.mustBelai("agent", "crew", "import", crew)

	runs := func(all bool) []poolRun {
		args := []string{"agent", "runs", "-json"}
		if all {
			args = append(args, "-all")
		}
		var rs []poolRun
		_ = json.Unmarshal([]byte(e.mustBelai(args...)), &rs)
		return rs
	}
	workers := func() []poolWorker {
		var ws []poolWorker
		_ = json.Unmarshal([]byte(e.mustBelai("agent", "ps", "-json")), &ws)
		return ws
	}
	auditLog := func(id string) string {
		out, _, _ := e.belai("crew", "log", "-run", id, "-audit", "-json")
		return out
	}
	t.Cleanup(func() {
		if t.Failed() {
			out, _, _ := e.belai("agent", "runs", "-all", "-json")
			t.Logf("runs: %s", out)
			out, _, _ = e.belai("agent", "ps", "-all", "-json")
			t.Logf("ps: %s", out)
			out, _, _ = e.belai("crew", "log", "-audit")
			t.Logf("crew log: %s", out)
			logs, _ := filepath.Glob(filepath.Join(e.home, "agents", "logs", "*"))
			for _, l := range logs {
				b, _ := os.ReadFile(l)
				t.Logf("%s:\n%s", filepath.Base(l), b)
			}
		}
		e.belai("agent", "stop", "-all")
	})

	// 1. The first model is down: the card is done on the second.
	mock.set("m1", true)
	card1 := strings.TrimSpace(e.mustBelai("kanban", "add", "Add hello.txt", "-label", "build"))
	e.mustBelai("agent", "start", "-trust-dir", "-crew", "t-pool", "-stay",
		"-pool", "openai/m1", "-pool", "openai/m2", "-switch-after", "1", "-final-retry", "8")
	rs := runs(false)
	if len(rs) != 1 || rs[0].State != "running" {
		t.Fatalf("runs %+v", rs)
	}
	run := rs[0].ID
	waitUntil(t, 90*time.Second, func() bool { return string(e.item(card1).List) == "review" }, "the first card done on m2")
	if !strings.Contains(auditLog(run), `"kind":"worker.fallback"`) {
		t.Fatalf("no worker.fallback in the crew log:\n%s", auditLog(run))
	}

	// 2. A person pins a model, then hands the worker back to the pool.
	ws := workers()
	if len(ws) != 1 {
		t.Fatalf("workers %+v", ws)
	}
	worker := ws[0].ID
	e.mustBelai("agent", "control", "-model", "openai/m3", worker)
	waitUntil(t, 30*time.Second, func() bool {
		ws := workers()
		return len(ws) == 1 && ws[0].Model == "m3"
	}, "the pinned model applied")
	e.mustBelai("agent", "control", "-auto", worker)
	waitUntil(t, 30*time.Second, func() bool {
		ws := workers()
		return len(ws) == 1 && slices.Contains([]string{"m1", "m2"}, ws[0].Model)
	}, "the worker back on the pool")

	// 3. The supervisor is killed: the run gets a new one and keeps running.
	old := runs(false)[0].Supervisor
	if old == 0 {
		t.Fatal("the run has no supervisor")
	}
	if err := syscall.Kill(old, syscall.SIGKILL); err != nil {
		t.Fatalf("kill the supervisor: %v", err)
	}
	waitUntil(t, 60*time.Second, func() bool {
		rs := runs(false)
		return len(rs) == 1 && rs[0].State == "running" && rs[0].Supervisor != 0 && rs[0].Supervisor != old
	}, "a new supervisor")

	// 4. The whole pool is down: the crew sleeps; a model comes back and the
	// pool recovers.
	mock.set("m2", true)
	card2 := strings.TrimSpace(e.mustBelai("kanban", "add", "Add hello.txt again", "-label", "build"))
	waitUntil(t, 90*time.Second, func() bool {
		rs := runs(false)
		return len(rs) == 1 && rs[0].PoolPhase == "sleeping"
	}, "the crew asleep")
	mock.set("m2", false)
	waitUntil(t, 90*time.Second, func() bool {
		rs := runs(false)
		return len(rs) == 1 && rs[0].PoolPhase == "" && strings.Contains(auditLog(run), `"outcome":"pool_recovered"`)
	}, "the pool recovered")
	waitUntil(t, 60*time.Second, func() bool { return string(e.item(card2).List) == "review" }, "the second card done")

	// 5. The pool fails again, and again after the final retry: the run ends
	// and says why, and its workers leave.
	mock.set("m2", true)
	e.mustBelai("kanban", "add", "Add hello.txt a third time", "-label", "build")
	var ended poolRun
	waitUntil(t, 120*time.Second, func() bool {
		for _, r := range runs(true) {
			if r.ID == run && r.State == "stopped" {
				ended = r
				return true
			}
		}
		return false
	}, "the run ended")
	if !strings.Contains(ended.Reason, "again after the final retry") {
		t.Fatalf("reason %q", ended.Reason)
	}
	if out := e.mustBelai("crew", "log", "-run", run, "-class", "pool_exhausted", "-json"); !strings.Contains(out, "pool_exhausted") {
		t.Fatalf("no pool_exhausted fault:\n%s", out)
	}
	waitUntil(t, 60*time.Second, func() bool { return len(workers()) == 0 }, "the workers gone")
}
