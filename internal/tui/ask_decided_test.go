package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/run"
)

// planDecider answers a plan review with fixed weights.
type planDecider struct {
	th      float64
	weights []float64
	asked   int
	ctxText string
}

func (p *planDecider) AskPolicy() (float64, bool) { return p.th, true }
func (p *planDecider) Choose(_ context.Context, _, ctx string, _ []string, _ map[string]string) ([]float64, error) {
	p.asked++
	p.ctxText = ctx
	return p.weights, nil
}
func (p *planDecider) Judge(context.Context, string, string) (float64, error) { return 0.5, nil }

func planReviewApp(t *testing.T, d agent.AskDecider) *App {
	t.Helper()
	_, wd := isolate(t)
	a := New(Options{Workdir: wd})
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 50})
	old := askDeciderFactory
	askDeciderFactory = func(config.Settings, run.CredentialSource) agent.AskDecider { return d }
	t.Cleanup(func() { askDeciderFactory = old })
	path := filepath.Join(wd, "plan.md")
	if err := os.WriteFile(path, []byte("# Ship it\n\n## Steps\n1. one\n2. two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.lastPrompt = &promptRec{input: "ship the release"}
	a.planReview = newPlanReviewState("plan", path)
	a.planReview.askID = "ask-1"
	a.push(viewPlanReview)
	return a
}

// A confident "keep planning" closes the review through the stay arm, records the
// choice as decided by Clef, and never reaches the plan's text.
func TestPlanReviewDecisionAboveTheBarIsApplied(t *testing.T) {
	d := &planDecider{th: 0.9, weights: []float64{0.02, 0.03, 0.95}}
	a := planReviewApp(t, d)
	cmd := a.decidePlanReviewCmd()
	if cmd == nil {
		t.Fatal("an open review with a decider must be put to it")
	}
	msg, ok := cmd().(planDecidedMsg)
	if !ok || msg.choice != planChoiceStay || msg.confidence != 0.95 {
		t.Fatalf("msg = %+v", msg)
	}
	if strings.Contains(d.ctxText, "one") || !strings.Contains(d.ctxText, "ship the release") || !strings.Contains(d.ctxText, "2 steps") {
		t.Fatalf("the model sees the request, the title and a step count, never the steps: %q", d.ctxText)
	}
	_ = a.handlePlanDecided(msg)
	if a.viewOpen(viewPlanReview) {
		t.Fatal("the decided choice must close the review")
	}
	var said bool
	for _, m := range a.messages {
		said = said || strings.Contains(m.Text(), "Clef decided the plan review: stay (confidence 0.95)")
	}
	if !said {
		t.Fatal("the thread must say Clef decided")
	}
}

// Below the bar nothing happens and the review stays for the user.
func TestPlanReviewDecisionBelowTheBarLeavesTheReview(t *testing.T) {
	a := planReviewApp(t, &planDecider{th: 0.9, weights: []float64{0.4, 0.3, 0.3}})
	if msg := a.decidePlanReviewCmd()(); msg != nil {
		t.Fatalf("msg = %+v, want none", msg)
	}
	if !a.viewOpen(viewPlanReview) {
		t.Fatal("the review must stay open")
	}
}

// A decision that arrives after the user answered, or for another review, or
// while a turn runs, is dropped.
func TestPlanReviewStaleDecisionIsDropped(t *testing.T) {
	a := planReviewApp(t, &planDecider{th: 0.9, weights: []float64{0.95, 0.03, 0.02}})
	good := planDecidedMsg{path: a.planReview.path, askID: "ask-1", choice: planChoiceApproveHere, confidence: 0.95}
	for name, m := range map[string]planDecidedMsg{
		"other ask":  {path: good.path, askID: "ask-2", choice: good.choice},
		"other path": {path: "/elsewhere/plan.md", askID: "ask-1", choice: good.choice},
	} {
		if cmd := a.handlePlanDecided(m); cmd != nil || !a.viewOpen(viewPlanReview) {
			t.Errorf("%s: applied a stale decision", name)
		}
	}
	a.planReview.noteMode = true
	if cmd := a.handlePlanDecided(good); cmd != nil || !a.viewOpen(viewPlanReview) {
		t.Error("a review the user is writing notes on must not be decided")
	}
}

// With no decider (the feature is off) the review is never put to anything.
func TestPlanReviewWithoutADeciderIsNeverDecided(t *testing.T) {
	a := planReviewApp(t, nil)
	askDeciderFactory = func(config.Settings, run.CredentialSource) agent.AskDecider { return nil }
	if a.decidePlanReviewCmd() != nil {
		t.Fatal("no decider, no decision")
	}
}
