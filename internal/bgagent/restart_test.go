package bgagent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/run"
)

// newLoopMockServer scripts the security, mode, and loop-evaluator classifiers
// and answers the main model with "ok". verdicts is the loop-evaluator reply
// sequence; once exhausted it returns STOP.
func newLoopMockServer(t *testing.T, verdicts []string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	idx := 0
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
		var system string
		for _, m := range req.Messages {
			if m.Role == "system" {
				system = m.Content
			}
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(system, "security classifier"):
			writeLoopJSON(w, "SAFE")
		case strings.Contains(system, "operating-mode classifier"):
			writeLoopJSON(w, "AGENT")
		case strings.Contains(system, "background-agent loop evaluator"):
			mu.Lock()
			i := idx
			idx++
			mu.Unlock()
			if i < len(verdicts) {
				writeLoopJSON(w, verdicts[i])
			} else {
				writeLoopJSON(w, "STOP")
			}
		default:
			// The main model turn is streamed; classifiers above are blocking.
			writeLoopSSE(w, "ok")
		}
	}))
}

func writeLoopJSON(w http.ResponseWriter, content string) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []any{map[string]any{
			"message":       map[string]any{"content": content, "role": "assistant"},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
	})
}

// writeLoopSSE emits a minimal OpenAI-style SSE stream carrying one content
// delta, which is what the streaming main-model turn consumes.
func writeLoopSSE(w http.ResponseWriter, content string) {
	w.Header().Set("Content-Type", "text/event-stream")
	flusher, _ := w.(http.Flusher)
	_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":" + jsonQuote(content) + "}}]}\n\n"))
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	if flusher != nil {
		flusher.Flush()
	}
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func newLoopManager(t *testing.T, verdicts []string) *Manager {
	t.Helper()
	server := newLoopMockServer(t, verdicts)
	t.Cleanup(server.Close)
	cfg := run.Config{Provider: "openai", BaseURL: server.URL + "/v1", APIKey: "test", Model: "gpt-4"}
	return NewManager(t.TempDir(), cfg, server.Client(), config.Settings{}, posture.Defaults())
}

func waitState(t *testing.T, m *Manager, name string, want State, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		inst, ok := m.Lookup(name)
		if ok {
			inst.mu.Lock()
			got := inst.State
			inst.mu.Unlock()
			if got == want {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatalf("agent %q never reached state %q", name, want)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func drainUntilClosed(t *testing.T, m *Manager, name string, timeout time.Duration) {
	t.Helper()
	inst, ok := m.Lookup(name)
	if !ok {
		return
	}
	deadline := time.After(timeout)
	for {
		select {
		case _, open := <-inst.Events:
			if !open {
				return
			}
		case <-deadline:
			return
		}
	}
}

func TestRunLoopModeContinueRestartsAndStops(t *testing.T) {
	m := newLoopManager(t, []string{"CONTINUE", "STOP"})
	profile := agentprofile.AgentProfile{
		Name: "loop", Description: "loop", SystemPrompt: "s",
		Mode: agentprofile.ModeLoop, MaxIterations: 1,
		Autonomy: agentprofile.AutonomyAutonomous,
	}
	if err := m.Start("loop", profile); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitState(t, m, "loop", StateDone, 10*time.Second)

	inst, _ := m.Lookup("loop")
	inst.mu.Lock()
	iter := inst.iteration
	state := inst.State
	inst.mu.Unlock()
	if iter != 2 {
		t.Fatalf("iteration = %d, want cumulative 2 across a CONTINUE restart", iter)
	}
	if state != StateDone {
		t.Fatalf("state = %q, want done", state)
	}
}

func TestRunLoopModeSupervisedContinuePauses(t *testing.T) {
	m := newLoopManager(t, []string{"CONTINUE", "STOP"})
	profile := agentprofile.AgentProfile{
		Name: "loop", Description: "loop", SystemPrompt: "s",
		Mode: agentprofile.ModeLoop, MaxIterations: 1,
		Autonomy: agentprofile.AutonomySupervised,
	}
	if err := m.Start("loop", profile); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// A supervised profile returning CONTINUE must park instead of looping
	// unattended.
	waitState(t, m, "loop", StatePaused, 15*time.Second)

	// Events channel must still be open while paused.
	inst, _ := m.Lookup("loop")
	select {
	case _, open := <-inst.Events:
		if !open {
			t.Fatal("events channel closed while paused")
		}
	default:
	}

	if err := m.Resume("loop"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	waitState(t, m, "loop", StateRunning, 15*time.Second)
	drainUntilClosed(t, m, "loop", 15*time.Second)
}

func TestRunLoopModeSleepDelays(t *testing.T) {
	m := newLoopManager(t, []string{"SLEEP", "STOP"})
	profile := agentprofile.AgentProfile{
		Name: "loop", Description: "loop", SystemPrompt: "s",
		Mode: agentprofile.ModeLoop, MaxIterations: 1,
		Schedule: "20ms", Autonomy: agentprofile.AutonomyAutonomous,
	}
	start := time.Now()
	if err := m.Start("loop", profile); err != nil {
		t.Fatalf("Start: %v", err)
	}
	drainUntilClosed(t, m, "loop", 15*time.Second)
	if elapsed := time.Since(start); elapsed < 10*time.Millisecond {
		t.Fatalf("SLEEP verdict completed too quickly: %v", elapsed)
	}
}

func TestRunLoopModeStopEnds(t *testing.T) {
	m := newLoopManager(t, []string{"STOP"})
	profile := agentprofile.AgentProfile{
		Name: "loop", Description: "loop", SystemPrompt: "s",
		Mode: agentprofile.ModeLoop, MaxIterations: 1,
		Autonomy: agentprofile.AutonomyAutonomous,
	}
	if err := m.Start("loop", profile); err != nil {
		t.Fatalf("Start: %v", err)
	}
	drainUntilClosed(t, m, "loop", 15*time.Second)
	waitState(t, m, "loop", StateDone, 15*time.Second)
}

func TestManagerPauseResume(t *testing.T) {
	m := newLoopManager(t, []string{"STOP"})
	profile := agentprofile.AgentProfile{
		Name: "loop", Description: "loop", SystemPrompt: "s",
		Mode: agentprofile.ModeLoop, MaxIterations: 1000,
		Autonomy: agentprofile.AutonomyAutonomous,
	}
	if err := m.Start("loop", profile); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitState(t, m, "loop", StateRunning, 15*time.Second)

	if err := m.Pause("loop"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	waitState(t, m, "loop", StatePaused, 15*time.Second)

	if err := m.Pause("loop"); err == nil {
		t.Fatal("expected error pausing an already-paused agent")
	}

	if err := m.Resume("loop"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	waitState(t, m, "loop", StateRunning, 5*time.Second)
	_ = m.Stop("loop")
}

// History must grow linearly across iterations, including an errored turn:
// without the lastOutput reset, a failed turn re-appends the previous reply.
func TestHistoryGrowsLinearlyAcrossTurns(t *testing.T) {
	// Fail the second logical turn persistently: the request for turn N carries
	// N user messages (one historical user turn per prior turn plus the new
	// prompt), so keying on the user-message count makes retries of the same
	// turn fail too.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		var system string
		users := 0
		for _, m := range req.Messages {
			if m.Role == "system" {
				system = m.Content
			}
			if m.Role == "user" {
				users++
			}
		}
		switch {
		case strings.Contains(system, "security classifier"):
			writeLoopJSON(w, "SAFE")
		case strings.Contains(system, "operating-mode classifier"):
			writeLoopJSON(w, "AGENT")
		default:
			if users == 2 {
				http.Error(w, "boom", http.StatusBadRequest)
				return
			}
			writeLoopSSE(w, "ok")
		}
	}))
	t.Cleanup(server.Close)

	m := NewManager(t.TempDir(), run.Config{
		Provider: "openai", BaseURL: server.URL + "/v1", APIKey: "test", Model: "gpt-4",
	}, server.Client(), config.Settings{}, posture.Defaults())

	inst := &AgentInstance{
		Profile: agentprofile.AgentProfile{
			Name: "lin", Description: "d", SystemPrompt: "s", Mode: agentprofile.ModeSingle,
		},
		Events: make(chan Event, 256),
	}

	for i := 0; i < 3; i++ {
		m.executeTurn(t.Context(), inst)
	}

	inst.mu.Lock()
	history := append([]run.Turn{}, inst.History...)
	inst.mu.Unlock()

	// Three user turns, and assistant turns only for the two successful calls.
	users := 0
	assistants := 0
	for _, turn := range history {
		switch turn.Role {
		case "user":
			users++
		case "assistant":
			assistants++
		}
	}
	if users != 3 {
		t.Fatalf("user turns = %d, want 3 (linear, not duplicated)", users)
	}
	if assistants != 2 {
		t.Fatalf("assistant turns = %d, want 2 (failed turn must not re-append stale output)", assistants)
	}
}
