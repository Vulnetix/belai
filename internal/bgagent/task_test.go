package bgagent

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/signet/internal/agentprofile"
	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/run"
)

// A task's prompt and attachments reach the agent's first turn, after the
// profile's own prompt.
func TestStartTaskCarriesPromptAndAttachments(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": "SAFE", "role": "assistant"}, "finish_reason": "stop"}},
		})
	}))
	defer srv.Close()
	m := NewManager(t.TempDir(), run.Config{Provider: "openai", BaseURL: srv.URL + "/v1", APIKey: "k", Model: "m"}, srv.Client(), config.Settings{}, posture.Defaults())

	profile := agentprofile.AgentProfile{Name: "deps", Description: "d", SystemPrompt: "You triage npm.", Mode: agentprofile.ModeSingle, MaxIterations: 6}
	task := Task{Prompt: "Check web/package.json.", Attachments: []run.Attachment{{Kind: "file", Label: "vulnetix sca", Body: "CVE-2021-23337 high pkg:npm/lodash@4.17.20"}}}
	if err := m.StartTask(t.TempDir(), "deps#1", profile, task); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	inst, _ := m.Lookup("deps#1")
	deadline := time.After(15 * time.Second)
	for {
		select {
		case _, open := <-inst.Events:
			if open {
				continue
			}
		case <-deadline:
			t.Fatal("agent did not finish")
		}
		break
	}
	mu.Lock()
	defer mu.Unlock()
	var turn string
	for _, b := range bodies {
		if strings.Contains(b, "You triage npm.") {
			turn = b
		}
	}
	if !strings.Contains(turn, "Check web/package.json.") || !strings.Contains(turn, "CVE-2021-23337") {
		t.Fatalf("first turn lacks the task or its attachment: %s", turn)
	}
	if inst.task.Prompt != "" || inst.task.Attachments != nil {
		t.Fatal("the task must be consumed by the first turn")
	}
}

func TestSessionRounds(t *testing.T) {
	for _, c := range []struct {
		p    agentprofile.AgentProfile
		want int
	}{
		{agentprofile.AgentProfile{Mode: agentprofile.ModeSingle, MaxIterations: 8}, 8},
		{agentprofile.AgentProfile{Mode: agentprofile.ModeSingle}, 1},
		{agentprofile.AgentProfile{Mode: agentprofile.ModeLoop, MaxIterations: 8}, 1},
	} {
		if got := sessionRounds(c.p); got != c.want {
			t.Errorf("sessionRounds(%s, %d) = %d, want %d", c.p.Mode, c.p.MaxIterations, got, c.want)
		}
	}
}
