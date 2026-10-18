package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/tui/components"
	"github.com/vulnetix/belai/internal/webask"
)

// m opens the model list over the plan; typing filters it, enter picks the
// highlighted model and esc goes back without changing the pick.
func TestPlanModelPickKeys(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	goldenPlanModel(a)
	a.view = viewPlanReview
	a.viewStack = []viewState{viewPlanReview}
	a.planReview = newPlanReviewState("plan", "/tmp/plan.md")
	a.planReview.models = goldenPlanModels()

	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})
	if !a.planReview.picking {
		t.Fatal("m must open the model list")
	}
	a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if a.planReview.picking || !a.planReview.model.zero() || a.view != viewPlanReview {
		t.Fatalf("esc must close the list only: picking=%v model=%v view=%d", a.planReview.picking, a.planReview.model, a.view)
	}

	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})
	for _, r := range "haiku" {
		a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if got := a.planPickMatches(); len(got) != 1 || got[0].Model != "claude-haiku-5-5" {
		t.Fatalf("filtered = %+v", got)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if a.planReview.picking || a.planReview.model != (modelTarget{"anthropic", "claude-haiku-5-5"}) {
		t.Fatalf("pick = %v, picking = %v", a.planReview.model, a.planReview.picking)
	}
	if a.planTargetWindow() != 200_000 {
		t.Fatalf("window = %d, want the picked model's", a.planTargetWindow())
	}
	if !strings.Contains(a.planModelLine(200), "anthropic/claude-haiku-5-5 (fast, 200k window)") {
		t.Fatalf("model line = %q", a.planModelLine(200))
	}
}

func TestPlanRouteNote(t *testing.T) {
	for _, c := range []struct {
		tokens, window int
		want           string
	}{
		{1234, 0, "≈ 1234 tok"},
		{127, 1_000_000, "≈ 127 tok · <1% of 1M"},
		{50_000, 1_000_000, "≈ 50k tok · 5% of 1M"},
		{170_000, 200_000, "≈ 170k tok · 85% of 200k"},
		{250_000, 200_000, "≈ 250k tok · won't fit 200k"},
	} {
		if got := planRouteNote(c.tokens, c.window); !strings.Contains(got, c.want) {
			t.Errorf("planRouteNote(%d, %d) = %q, want %q", c.tokens, c.window, got, c.want)
		}
	}
}

// openSyncedPlanReview raises a plan review on a synced session, as a plan
// turn's plan file does.
func openSyncedPlanReview(t *testing.T, a *App) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plan-demo.md")
	if err := os.WriteFile(path, []byte("# Plan: demo\n\n## Summary\n\nDo it.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.Update(agentEventMsg{Kind: agent.EventPlanFileKind, PlanName: "plan-demo", PlanPath: path})
	asks := entriesOfType(t, a, "ask")
	if len(asks) != 1 {
		t.Fatalf("ask entries = %d, want 1", len(asks))
	}
	return asks[0].ID
}

// The plan-review ask offers the session's model first and the estimate of
// each approve route.
func TestPlanReviewAskOffersModelsAndEstimates(t *testing.T) {
	a, _ := newSyncApp(t)
	goldenPlanModel(a)
	openSyncedPlanReview(t, a)
	meta := entriesOfType(t, a, "ask")[0].Meta
	raw, _ := json.Marshal(meta["models"])
	var models []webask.PlanModel
	_ = json.Unmarshal(raw, &models)
	if len(models) == 0 || models[0].Provider != "anthropic" || models[0].Model != "claude-opus-5-5" || !models[0].Current {
		t.Fatalf("models = %s, want the session's model first", raw)
	}
	raw, _ = json.Marshal(meta["estimates"])
	var est struct{ Plan, New, Here int }
	_ = json.Unmarshal(raw, &est)
	if est.Plan == 0 || est.New < est.Plan || est.Here < est.Plan {
		t.Fatalf("estimates = %s", raw)
	}
}

// A web approval may only pick a model the review offered; the one it picked
// is recorded with the answer.
func TestWebPlanAnswerPicksAnOfferedModel(t *testing.T) {
	a, rec := newSyncApp(t)
	goldenPlanModel(a)
	askID := openSyncedPlanReview(t, a)
	a.planReview.models = goldenPlanModels()

	bad, _ := json.Marshal(map[string]any{"choice": "approve_here", "model": map[string]string{"provider": "evil", "model": "x"}})
	a.handleRemoteAnswer(sessionsync.RemoteAnswer{ID: "w1", SessionID: a.sessionID, AskID: askID, Kind: askPlanReview, Payload: bad})
	if got := rec.ack(t, "answer:w1"); got["status"] != sessionsync.AckRefused || !strings.Contains(got["reason"], "not offered") {
		t.Fatalf("ack = %v, want refused: not offered", got)
	}

	good, _ := json.Marshal(map[string]any{"choice": "approve_here", "model": map[string]string{"provider": "anthropic", "model": "claude-haiku-5-5"}})
	a.handleRemoteAnswer(sessionsync.RemoteAnswer{ID: "w2", SessionID: a.sessionID, AskID: askID, Kind: askPlanReview, Payload: good})
	if got := rec.ack(t, "answer:w2"); got["status"] != sessionsync.AckAccepted {
		t.Fatalf("ack = %v, want accepted", got)
	}
	answers := entriesOfType(t, a, "ask_answer")
	if len(answers) != 1 || answers[0].Meta["plan_model"] != "anthropic/claude-haiku-5-5" {
		t.Fatalf("ask_answer = %+v", answers)
	}
}

// Approving in a new session starts the child from the plan alone: a
// compacted parent's summary is not carried.
func TestApproveNewDropsTheSummary(t *testing.T) {
	a := planReviewApp(t, nil)
	a.status.Configured = true
	a.summary = "an earlier compaction"
	// The fork copies the saved parent, so the parent needs a saved turn.
	a.messages = append(a.messages, components.Message{Role: "user", Content: "plan the release"})
	a.persistTailMode(true)
	a.submitPlanApproveNew()
	for _, m := range a.messages {
		if strings.Contains(m.Text(), "fork failed") {
			t.Fatal(m.Text())
		}
	}
	if a.summary != "" {
		t.Fatalf("summary = %q, want none in the new session", a.summary)
	}
}

// The estimate takes the system prompt and tools from the last main-turn call
// (a tool-less probe does not stand in for it), and approving here never reads
// smaller than what that call already sent.
func TestPlanEstimateFromTheLastMainTurnCall(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.intelReq.add(run.UsageEvent{Role: run.RoleAgent, Request: run.RequestShape{System: 1356, ToolDefs: 4197, History: 2683, ToolResults: map[string]int{"Read": 400}}})
	a.intelReq.add(run.UsageEvent{Role: run.RoleAgent, Request: run.RequestShape{System: 19, History: 7}})
	e := a.planEstimate("plan")
	if e.Base != 1356+4197 {
		t.Fatalf("base = %d, want the main-turn call's system and tools", e.Base)
	}
	if e.Here < 1356+4197+2683+400 || e.Here <= e.New {
		t.Fatalf("here = %d, new = %d: here must cover what the last call sent", e.Here, e.New)
	}
}
