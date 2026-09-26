package bgagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/agentpool"
	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/run"
)

// monitorMockServer answers the trigger-monitor evaluation with the supplied
// reply and streams a minimal main-model turn for everything else.
func monitorMockServer(t *testing.T, reply string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/nonces") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var system, user string
		for _, m := range req.Messages {
			switch m.Role {
			case "system":
				system = m.Content
			case "user":
				user += m.Content
			}
		}
		switch {
		case strings.Contains(system, "security classifier"):
			writeLoopJSON(w, "SAFE")
		case strings.Contains(system, "operating-mode classifier"):
			writeLoopJSON(w, "AGENT")
		case strings.Contains(user, "trigger monitor"):
			writeLoopJSON(w, reply)
		default:
			writeLoopSSE(w, "ok")
		}
	}))
}

func monitorManager(t *testing.T, reply string) (*Manager, *httptest.Server) {
	t.Helper()
	server := monitorMockServer(t, reply)
	t.Cleanup(server.Close)
	cfg := run.Config{Provider: "openai", BaseURL: server.URL + "/v1", APIKey: "test", Model: "gpt-4"}
	return NewManager(t.TempDir(), cfg, server.Client(), config.Settings{}, posture.Defaults()), server
}

func newBareInstance(name string) *AgentInstance {
	return &AgentInstance{
		Profile: agentprofile.AgentProfile{
			Name: name, Description: "d", SystemPrompt: "s", Mode: agentprofile.ModeSingle,
		},
		Events: make(chan Event, 256),
	}
}

// TestEvaluateMonitorYesNoError covers the trigger-monitor classifier: a YES
// reply triggers, a NO reply does not, and a transport error propagates.
func TestEvaluateMonitorYesNoError(t *testing.T) {
	t.Run("yes", func(t *testing.T) {
		m, _ := monitorManager(t, "YES")
		ok, err := m.evaluateMonitor(context.Background(), "something changed")
		if err != nil {
			t.Fatalf("evaluateMonitor: %v", err)
		}
		if !ok {
			t.Fatal("YES reply should trigger the monitor")
		}
	})
	t.Run("no", func(t *testing.T) {
		m, _ := monitorManager(t, "NO")
		ok, err := m.evaluateMonitor(context.Background(), "something changed")
		if err != nil {
			t.Fatalf("evaluateMonitor: %v", err)
		}
		if ok {
			t.Fatal("NO reply should not trigger the monitor")
		}
	})
	t.Run("error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}))
		t.Cleanup(server.Close)
		m := NewManager(t.TempDir(), run.Config{
			Provider: "openai", BaseURL: server.URL + "/v1", APIKey: "test", Model: "gpt-4",
		}, server.Client(), config.Settings{}, posture.Defaults())
		if ok, err := m.evaluateMonitor(context.Background(), "x"); err == nil || ok {
			t.Fatalf("evaluateMonitor = (%v, %v), want error", ok, err)
		}
	})
}

// TestAcquireReleaseLeaseWithPool covers the pooled half of acquireLease and
// releaseLease: a free slot admits immediately, emits queued→running roster
// deltas, and Done emits the terminal delta.
func TestAcquireReleaseLeaseWithPool(t *testing.T) {
	m, _ := monitorManager(t, "NO")
	m.SetPool(agentpool.New(1))

	inst := newBareInstance("bg-agent")
	runCtx, lease, ok := m.acquireLease(context.Background(), inst)
	if !ok {
		t.Fatal("acquireLease should admit an idle pool")
	}
	if lease == nil {
		t.Fatal("expected a lease for a non-nil pool")
	}
	if runCtx != lease.Context() {
		t.Fatal("runCtx should be the lease's derived context")
	}

	var states []string
drain:
	for {
		select {
		case e := <-inst.Events:
			if e.Kind == agent.EventSubagentKind && e.Subagent != nil {
				states = append(states, e.Subagent.State)
			}
		default:
			break drain
		}
	}
	if len(states) < 2 || states[0] != "queued" || states[1] != "running" {
		t.Fatalf("roster states = %v, want [queued running]", states)
	}

	m.releaseLease(inst, lease)
	select {
	case e := <-inst.Events:
		if e.Kind != agent.EventSubagentKind || e.Subagent == nil || e.Subagent.State != "done" {
			t.Fatalf("release event = %+v, want done", e)
		}
	case <-time.After(time.Second):
		t.Fatal("releaseLease did not emit a terminal roster delta")
	}
}

// TestAcquireLeaseCancelledWhileQueued covers the dropped-while-queued branch:
// a pool whose only slot is held forces the background turn to queue, and a
// cancelled context drops it with a cancelled roster delta.
func TestAcquireLeaseCancelledWhileQueued(t *testing.T) {
	m, _ := monitorManager(t, "NO")
	p := agentpool.New(1)
	m.SetPool(p)

	held, err := p.Acquire(context.Background(), agentpool.Handle{ID: "holder", Label: "holder", Kind: "background"})
	if err != nil {
		t.Fatalf("acquire holder: %v", err)
	}
	defer held.Done(agentpool.StateDone, "")

	ctx, cancel := context.WithCancel(context.Background())
	inst := newBareInstance("bg-agent")

	done := make(chan bool, 1)
	go func() {
		_, _, ok := m.acquireLease(ctx, inst)
		done <- ok
	}()

	// Wait for the turn to queue, then cancel.
	deadline := time.After(2 * time.Second)
	for {
		queued := false
		for _, h := range p.Snapshot() {
			if h.ID == "bg:bg-agent" && h.State == agentpool.StateQueued {
				queued = true
				break
			}
		}
		if queued {
			break
		}
		select {
		case <-deadline:
			t.Fatal("background turn never queued")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()

	if ok := <-done; ok {
		t.Fatal("cancelled queued acquire must not be granted")
	}

	// The cancelled roster delta must have been emitted.
	found := false
	for {
		select {
		case e := <-inst.Events:
			if e.Kind == agent.EventSubagentKind && e.Subagent != nil && e.Subagent.State == "cancelled" {
				found = true
			}
		default:
			goto checked
		}
	}
checked:
	if !found {
		t.Fatal("expected a cancelled roster delta")
	}
}

// TestRunMonitorTriggersTurn covers runMonitor end to end: a YES reply fires a
// turn whose EventDoneKind is observable on the events channel.
func TestRunMonitorTriggersTurn(t *testing.T) {
	m, _ := monitorManager(t, "YES")
	profile := agentprofile.AgentProfile{
		Name: "mon", Description: "mon", SystemPrompt: "s",
		Mode: agentprofile.ModeMonitor, Schedule: "20ms", MonitorCondition: "vulns found",
	}
	if err := m.Start("mon", profile); err != nil {
		t.Fatalf("Start: %v", err)
	}
	inst, ok := m.Lookup("mon")
	if !ok {
		t.Fatal("agent not found after start")
	}

	deadline := time.After(10 * time.Second)
	for {
		select {
		case e, open := <-inst.Events:
			if !open {
				t.Fatal("events closed before a turn completed")
			}
			if e.Kind == agent.EventDoneKind {
				_ = m.Stop("mon")
				return
			}
		case <-deadline:
			t.Fatal("monitor never fired a turn within timeout")
		}
	}
}
