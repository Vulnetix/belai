package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/signet/internal/agentprofile"
	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/modes"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/tools"
)

// TestGroundingProbeCollectsLayoutAndAgentsMD pins the non-git parts of the
// grounding probe: a bounded top-level listing and AGENTS.md content.
func TestGroundingProbeCollectsLayoutAndAgentsMD(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# AGENTS\nbe careful"), 0o600)
	_ = os.MkdirAll(filepath.Join(root, "subdir"), 0o755)

	s := &Session{workdir: root}
	g := s.groundingProbe(context.Background())

	if !strings.Contains(g.Layout, "subdir/") {
		t.Fatalf("layout missing subdir/: %q", g.Layout)
	}
	if !strings.Contains(g.Layout, "AGENTS.md") {
		t.Fatalf("layout missing AGENTS.md: %q", g.Layout)
	}
	if g.AgentsMD == "" || !strings.Contains(g.AgentsMD, "be careful") {
		t.Fatalf("AGENTS.md not collected: %q", g.AgentsMD)
	}
}

// TestGroundingDigestOmitsEmptySections pins that a zero grounding renders no
// evidence rather than a stub prompt, and never carries a Go fmt artefact.
func TestGroundingDigestOmitsEmptySections(t *testing.T) {
	s := &Session{workdir: t.TempDir()}
	g := s.groundingProbe(context.Background())
	d := g.digest()
	if strings.Contains(d, "<nil>") || strings.Contains(d, "%!") {
		t.Fatalf("digest contains a Go fmt artefact: %q", d)
	}
}

// TestRelevantAgentsFiltersNonSingle pins the grounding rule: only flat,
// single-shot background agents are listed; scheduled/loop/monitor
// definitions are background processes, not evidence.
func TestRelevantAgentsFiltersNonSingle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SIGNET_HOME", home)

	mustSave := func(p agentprofile.AgentProfile) {
		if _, err := agentprofile.Save(p); err != nil {
			t.Fatalf("save %s: %v", p.Name, err)
		}
	}
	mustSave(agentprofile.AgentProfile{Name: "plain", Description: "d", SystemPrompt: "s", Mode: agentprofile.ModeSingle})
	mustSave(agentprofile.AgentProfile{Name: "loop", Description: "d", SystemPrompt: "s", Mode: agentprofile.ModeLoop})
	mustSave(agentprofile.AgentProfile{Name: "sched", Description: "d", SystemPrompt: "s", Mode: agentprofile.ModeScheduled, Schedule: "*/5 * * * *"})
	mustSave(agentprofile.AgentProfile{Name: "mon", Description: "d", SystemPrompt: "s", Mode: agentprofile.ModeMonitor, MonitorCondition: "x"})

	names := relevantAgents()
	if len(names) != 1 || names[0] != "plain" {
		t.Fatalf("relevantAgents = %v, want [plain]", names)
	}
}

// TestExploreSubagentResetsOnSteer pins the reset-on-steer business rule: an
// explore subagent that exhausts its iteration budget does not hard-fail when
// new steering arrives — the budget resets and the subagent keeps going.
func TestExploreSubagentResetsOnSteer(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o600)

	newServer := func() (*httptest.Server, *int64) {
		var calls int64
		var mu sync.Mutex
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
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
			switch {
			case strings.Contains(system, "security classifier"):
				writeChatJSON(w, "SAFE")
			default:
				mu.Lock()
				calls++
				mu.Unlock()
				writeToolCallJSON(w, "Read", `{"path":"f.txt"}`)
			}
		}))
		return srv, &calls
	}

	newSession := func(srv *httptest.Server, explore bool, steerSource func() string) (*Session, error) {
		sess, err := NewSession(Options{
			Cfg:           run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "test"},
			Client:        srv.Client(),
			Registry:      tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024}),
			Posture:       posture.Defaults(),
			MaxIterations: 2,
			SkipNonceSeed: true,
			Settings:      config.Settings{Resilience: &config.ResilienceSettings{MaxPasses: 1}},
		})
		if err != nil {
			return nil, err
		}
		sess.exploreSubagent = explore
		sess.steerSource = steerSource
		return sess, nil
	}

	// Case A: no steering source — one pass of 2 iterations, then error.
	srvA, callsA := newServer()
	sessA, err := newSession(srvA, false, nil)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	_, err = sessA.run(context.Background(), nil, TurnInput{Prompt: "read it", Mode: rolemanager.ModeDecision{Mode: modes.ModeAgent}}, false, func(Event) {})
	if err != nil {
		t.Fatalf("case A: budget exhaustion must not be an error, got %v", err)
	}
	srvA.Close()
	if *callsA != 4 {
		t.Fatalf("case A: expected 4 model calls (initial pass + one continuation), got %d", *callsA)
	}

	// Case B: steering arrives only after the first pass exhausts — the
	// budget resets and a second pass runs before the final error.
	srvB, callsB := newServer()
	var steered bool
	sessB, err := newSession(srvB, true, func() string {
		if !steered && *callsB >= 2 {
			steered = true
			return "now check f.txt too"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	_, err = sessB.run(context.Background(), nil, TurnInput{Prompt: "read it", Mode: rolemanager.ModeDecision{Mode: modes.ModeAgent}}, false, func(Event) {})
	if err != nil {
		t.Fatalf("case B: budget exhaustion must not be an error, got %v", err)
	}
	srvB.Close()
	if !steered {
		t.Fatal("case B: steering source was never drained")
	}
	if *callsB != 6 {
		t.Fatalf("case B: expected 6 model calls (initial + steer reset + one continuation), got %d", *callsB)
	}
}

// TestPlanExploreLifecycleEvents pins the Phase 3 contract: a forced plan-mode
// fan-out emits one lifecycle event per survey task with the right Index/Total,
// activity events carry the subagent's ID, and every subagent event precedes
// the first parent text delta.
func TestPlanExploreLifecycleEvents(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o600)

	srv := mockSecurityServer("Read", `{"path":"f.txt"}`, "finding")
	defer srv.Close()

	sess, err := NewSession(Options{
		Cfg:           run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "test"},
		Client:        srv.Client(),
		Registry:      tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024}),
		Posture:       posture.Defaults(),
		SkipNonceSeed: true,
		AllowExplore:  true,
		Settings:      config.Settings{Resilience: &config.ResilienceSettings{MaxExploreIterations: 2}},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	var events []Event
	_, err = sess.run(context.Background(), nil, TurnInput{Prompt: "survey the repo", ForceMode: modes.ModePlan}, false, func(e Event) {
		events = append(events, e)
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	lifecycle := map[string][]string{} // id -> ordered states
	activityIDs := map[string]bool{}
	firstParentText := -1
	lastSubagent := -1
	for i, e := range events {
		switch e.Kind {
		case EventSubagentKind:
			if e.Subagent == nil {
				t.Fatalf("subagent event without update: %+v", e)
			}
			if e.Subagent.Total != 3 {
				t.Fatalf("subagent %s Total = %d, want 3", e.Subagent.ID, e.Subagent.Total)
			}
			lifecycle[e.Subagent.ID] = append(lifecycle[e.Subagent.ID], e.Subagent.State)
			lastSubagent = i
		case EventSubagentActivityKind:
			activityIDs[e.SubagentID] = true
			lastSubagent = i
		case EventTextKind:
			if firstParentText == -1 {
				firstParentText = i
			}
		}
	}

	// One queued, running and done event for each of the three survey tasks.
	for _, id := range []string{"e1", "e2", "e3"} {
		got := lifecycle[id]
		if len(got) != 3 || got[0] != "queued" || got[1] != "running" || got[2] != "done" {
			t.Fatalf("subagent %s lifecycle = %v, want [queued running done]", id, got)
		}
	}
	if len(lifecycle) != 3 {
		t.Fatalf("expected exactly 3 subagents, got %d", len(lifecycle))
	}
	if !activityIDs["e1"] || !activityIDs["e2"] || !activityIDs["e3"] {
		t.Fatalf("activity events missing a subagent ID: %v", activityIDs)
	}
	if firstParentText == -1 {
		t.Fatal("expected a parent text event")
	}
	if lastSubagent >= firstParentText {
		t.Fatalf("a subagent event (%d) landed after the first parent text (%d)", lastSubagent, firstParentText)
	}
}

// TestPlanExploreDisabledSkipsFanOut pins the resilience.plan_explore: false
// gate: a forced plan-mode turn goes straight to planning with no subagent
// events and no clarify.
func TestPlanExploreDisabledSkipsFanOut(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o600)

	srv := mockSecurityServer("Read", `{"path":"f.txt"}`, "plan ready")
	defer srv.Close()

	f := false
	sess, err := NewSession(Options{
		Cfg:           run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "test"},
		Client:        srv.Client(),
		Registry:      tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024}),
		Posture:       posture.Defaults(),
		SkipNonceSeed: true,
		AllowExplore:  true,
		Settings:      config.Settings{Resilience: &config.ResilienceSettings{PlanExplore: &f}},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	var events []Event
	_, err = sess.run(context.Background(), nil, TurnInput{Prompt: "plan the refactor", ForceMode: modes.ModePlan}, false, func(e Event) {
		events = append(events, e)
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	for _, e := range events {
		if e.Kind == EventSubagentKind || e.Kind == EventSubagentActivityKind || e.Kind == EventClarifyAskKind {
			t.Fatalf("plan_explore: false must produce no fan-out or clarify, got %v", e.Kind)
		}
	}
}
