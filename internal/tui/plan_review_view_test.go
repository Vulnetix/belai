package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/config"
)

func TestPlanReviewHeightFloor(t *testing.T) {
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})
	a.planReview = newPlanReviewState("plan", "/tmp/plan.md")
	a.width = 80
	a.height = 5
	if h := a.planReviewHeight(); h != 5 {
		t.Fatalf("height floor = %d, want 5", h)
	}
}

func TestPlanReviewViewContainsPathAndOptions(t *testing.T) {
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})
	a.planReview = newPlanReviewState("plan-fix", "/tmp/plan-fix.md")
	a.planReview.vp.Width = a.planReviewWidth()
	a.planReview.vp.Height = a.planReviewHeight()
	a.planReview.vp.SetContent("Plan:\n1. do it")
	v := a.planReviewView()
	if !strings.Contains(v, "/tmp/plan-fix.md") {
		t.Fatalf("view missing plan path:\n%s", v)
	}
	for _, want := range []string{"approve", "refine", "cancel"} {
		if !strings.Contains(v, want) {
			t.Fatalf("view missing option %q:\n%s", want, v)
		}
	}
}

func TestPlanReviewKeyNavigation(t *testing.T) {
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})
	a.view = viewPlanReview
	a.viewStack = []viewState{viewPlanReview}
	a.planReview = newPlanReviewState("plan", "/tmp/plan.md")

	a.Update(tea.KeyMsg{Type: tea.KeyDown})
	if a.planReview.selected != planReviewRefine {
		t.Fatalf("down: selected = %d, want refine", a.planReview.selected)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyDown})
	if a.planReview.selected != planReviewCancel {
		t.Fatalf("down: selected = %d, want cancel", a.planReview.selected)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyUp})
	if a.planReview.selected != planReviewRefine {
		t.Fatalf("up: selected = %d, want refine", a.planReview.selected)
	}
}

func TestPlanReviewCancelKeepsMode(t *testing.T) {
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})
	a.mode = "plan"
	a.modeSticky = true
	a.view = viewPlanReview
	a.viewStack = []viewState{viewPlanReview}
	a.planReview = newPlanReviewState("plan", "/tmp/plan.md")

	a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if a.view != viewChat {
		t.Fatalf("view = %d, want chat", a.view)
	}
	if a.mode != "plan" || a.modeSticky != true {
		t.Fatalf("cancel changed mode: mode=%q sticky=%v", a.mode, a.modeSticky)
	}
	if a.state.ActivePlan != "" {
		t.Fatalf("cancel set ActivePlan")
	}
}

func TestPlanReviewApproveSetsActivePlan(t *testing.T) {
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})
	a.status.Configured = true
	a.mode = "plan"
	a.modeSticky = true
	a.view = viewPlanReview
	a.viewStack = []viewState{viewPlanReview}
	a.planReview = newPlanReviewState("approved", "/tmp/approved.md")

	_, cmd := a.handlePlanReviewKey(tea.KeyMsg{Type: tea.KeyEnter}) // Approve
	if cmd == nil {
		t.Fatal("Approve should submit a turn")
	}
	st, _ := config.LoadState()
	if st.ActivePlan != "approved" {
		t.Fatalf("ActivePlan = %q, want approved", st.ActivePlan)
	}
	// Reset state so later tests are not affected.
	st.ActivePlan = ""
	_ = config.SaveState(st)
}
