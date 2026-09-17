package bgagent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/signet/internal/agent"
	"github.com/vulnetix/signet/internal/agentprofile"
	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/run"
)

func TestManagerStartStop(t *testing.T) {
	m := newTestManager(t)
	profile := agentprofile.AgentProfile{
		Name:         "test-agent",
		Description:  "test",
		SystemPrompt: "You are a test.",
		Mode:         agentprofile.ModeSingle,
	}
	if err := m.Start("test-agent", profile); err != nil {
		t.Fatalf("Start: %v", err)
	}
	list := m.List()
	if len(list) != 1 || list[0].Name != "test-agent" {
		t.Fatalf("unexpected list: %v", list)
	}
	if err := m.Stop("test-agent"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if len(m.List()) != 0 {
		t.Fatalf("expected 0 agents after stop, got %d", len(m.List()))
	}
}

func TestManagerStopUnknown(t *testing.T) {
	m := newTestManager(t)
	if err := m.Stop("nope"); err == nil {
		t.Fatal("expected error for unknown agent")
	}
}

func TestManagerDuplicateStart(t *testing.T) {
	m := newTestManager(t)
	profile := agentprofile.AgentProfile{
		Name: "dup", Description: "d", SystemPrompt: "s", Mode: agentprofile.ModeSingle,
	}
	if err := m.Start("dup", profile); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if err := m.Start("dup", profile); err == nil {
		t.Fatal("expected error for duplicate start")
	}
}

func TestManagerLoopMaxIterations(t *testing.T) {
	m := newTestManager(t)
	profile := agentprofile.AgentProfile{
		Name: "loop-bot", Description: "loop", SystemPrompt: "loop",
		Mode: agentprofile.ModeLoop, MaxIterations: 2,
	}
	if err := m.Start("loop-bot", profile); err != nil {
		t.Fatalf("Start: %v", err)
	}
	inst, ok := m.Lookup("loop-bot")
	if !ok {
		t.Fatal("agent not found after start")
	}
	var done bool
	deadline := time.After(10 * time.Second)
eventLoop:
	for {
		select {
		case e, ok := <-inst.Events:
			if !ok {
				break eventLoop
			}
			if e.Kind == agent.EventDoneKind {
				done = true
				break eventLoop
			}
		case <-deadline:
			break eventLoop
		}
	}
	if !done {
		t.Fatal("agent did not complete within timeout")
	}
	inst.mu.Lock()
	iter := inst.iteration
	inst.mu.Unlock()
	if iter != 2 {
		t.Fatalf("iteration = %d, want 2", iter)
	}
	_ = m.Stop("loop-bot")
}

func TestManagerScheduledTick(t *testing.T) {
	m := newTestManager(t)
	profile := agentprofile.AgentProfile{
		Name: "sched-bot", Description: "sched", SystemPrompt: "sched",
		Mode: agentprofile.ModeScheduled, Schedule: "50ms",
	}
	if err := m.Start("sched-bot", profile); err != nil {
		t.Fatalf("Start: %v", err)
	}
	inst, ok := m.Lookup("sched-bot")
	if !ok {
		t.Fatal("agent not found after start")
	}
	sawEvent := false
	deadline := time.After(5 * time.Second)
tickLoop:
	for {
		select {
		case _, ok := <-inst.Events:
			if !ok {
				break tickLoop
			}
			sawEvent = true
			break tickLoop
		case <-deadline:
			break tickLoop
		}
	}
	if !sawEvent {
		t.Fatal("scheduled agent did not fire within timeout")
	}
	_ = m.Stop("sched-bot")
}

func TestManagerCancellation(t *testing.T) {
	m := newTestManager(t)
	profile := agentprofile.AgentProfile{
		Name: "cancel-bot", Description: "c", SystemPrompt: "c",
		Mode: agentprofile.ModeLoop, MaxIterations: 100,
	}
	if err := m.Start("cancel-bot", profile); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := m.Stop("cancel-bot"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	_, ok := m.Lookup("cancel-bot")
	if ok {
		t.Fatalf("agent still present after stop")
	}
}

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	server := newMockServer(t)
	t.Cleanup(server.Close)
	cfg := run.Config{
		Provider: "openai", BaseURL: server.URL + "/v1", APIKey: "test", Model: "gpt-4",
	}
	settings := config.Settings{}
	return NewManager(t.TempDir(), cfg, server.Client(), settings, posture.Defaults())
}

func newMockServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/nonces":
			w.WriteHeader(http.StatusNotFound)
			return
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{
					map[string]any{
						"message":       map[string]any{"content": "SAFE", "role": "assistant"},
						"finish_reason": "stop",
					},
				},
				"usage": map[string]any{
					"prompt_tokens":     1,
					"completion_tokens": 1,
					"total_tokens":      2,
				},
			})
		default:
			if strings.Contains(r.URL.Path, "/nonces") {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			http.NotFound(w, r)
		}
	}))
}

// A turn that fails never reaches the EventDoneKind assignment that sets
// lastOutput, so without a reset at the top of executeTurn the previous turn's
// reply is carried forward and appended to History a second time.
func TestExecuteTurnDiscardsPreviousOutputOnFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	m := NewManager(t.TempDir(), run.Config{
		Provider: "openai", BaseURL: server.URL + "/v1", APIKey: "test", Model: "gpt-4",
	}, server.Client(), config.Settings{}, posture.Defaults())

	inst := &AgentInstance{
		Profile: agentprofile.AgentProfile{
			Name: "carry", Description: "d", SystemPrompt: "s", Mode: agentprofile.ModeSingle,
		},
		Events: make(chan Event, 256),
	}
	inst.lastOutput = "stale reply from the previous turn"

	m.executeTurn(t.Context(), inst)

	if got := inst.LastOutput(); got != "" {
		t.Fatalf("lastOutput = %q, want empty after a failed turn", got)
	}
	for _, turn := range inst.History {
		if strings.Contains(turn.Content, "stale reply") {
			t.Fatalf("failed turn re-appended the previous reply: %+v", inst.History)
		}
	}
}
