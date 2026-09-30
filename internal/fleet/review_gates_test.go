package fleet

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/testdetect"
	"github.com/vulnetix/belai/internal/testrun"
	"github.com/vulnetix/belai/internal/tools"
)

func gatedReviewer() agentprofile.AgentProfile {
	return agentprofile.AgentProfile{
		Name: "t-reviewer", Description: "d", SystemPrompt: "sp", Mode: agentprofile.ModeWorker,
		Tools: []string{"Read"},
		Kanban: &agentprofile.KanbanSpec{
			Lists: []string{"review"}, Labels: []string{"needs-review"},
			OnSuccess: agentprofile.Route{List: "done", DropLabels: []string{"needs-review"}},
			OnFailure: agentprofile.Route{List: "backlog", Labels: []string{"build"}, DropLabels: []string{"needs-review"}},
			Gates:     &agentprofile.GatesSpec{Verify: agentprofile.VerifyEnforce, Review: true},
			Project:   "all",
		},
		Workspace: &agentprofile.WorkspaceSpec{Isolation: agentprofile.IsolationWorktree},
		Autonomy:  "autonomous", Budget: &agentprofile.BudgetSpec{MaxPassesPerItem: 2},
	}
}

var reviewGates = []kanban.Gate{
	{Title: "suite passes", Kind: kanban.GateRunnable, Suite: "go"},
	{Title: "wording reviewed", Kind: kanban.GateManual},
}

// reviewHarness files a card waiting for review and runs a reviewer whose turn
// is decide.
func reviewHarness(t *testing.T, results map[string]testrun.Result, decide func(t Turn, gate tools.KanbanGate)) (kanban.Item, *kanban.Store) {
	t.Helper()
	store, reg := testEnv(t)
	it, _, err := store.Add(kanban.ItemInput{Title: "review me", Labels: []string{"needs-review"}, List: kanban.Review, Gates: reviewGates}, kanban.Provenance{})
	if err != nil {
		t.Fatal(err)
	}
	w := newWorker(t, store, reg, gatedReviewer(), func(ctx context.Context, tt Turn) (run.Result, error) {
		if decide != nil {
			decide(tt, tools.KanbanGate{KanbanBase: tools.KanbanBase{Store: store, Claim: tt.Claim}})
		}
		return complete(ctx, tt)
	})
	w.Repo = gitRepo(t)
	w.Suites = func(context.Context) []testdetect.Suite {
		return []testdetect.Suite{{Name: "go", Ecosystem: "go", Command: []string{"go", "test", "./..."}}}
	}
	w.RunTests = func(_ context.Context, plan testrun.Plan) []testrun.Result {
		var out []testrun.Result
		for i, name := range plan.Names {
			r, ok := results[name]
			if !ok {
				r = testrun.Result{Status: testrun.Passed, Output: "ok  \tp\t0.01s\n"}
			}
			r.Suite, r.Command = name, plan.Argvs[i]
			out = append(out, r)
		}
		return out
	}
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got, store
}

func decide(gate, state string) func(Turn, tools.KanbanGate) {
	return func(_ Turn, k tools.KanbanGate) {
		_, _ = k.Execute(context.Background(), map[string]any{"gate": gate, "state": state, "evidence": "checked"})
	}
}

func TestReviewerClosesACardOnlyWhenEveryGateIsMet(t *testing.T) {
	got, _ := reviewHarness(t, nil, decide("G2", "met"))
	if got.List != kanban.Done {
		t.Fatalf("runnable verified by the harness and manual met by the reviewer: %s %q", got.List, got.LastNote())
	}
	for _, id := range []string{"G1", "G2"} {
		if g, _ := got.GateByID(id); g.State != kanban.GateMet {
			t.Fatalf("%s %+v", id, g)
		}
	}
}

func TestReviewerThatLeavesAManualGateUndecidedFailsTheAttempt(t *testing.T) {
	got, _ := reviewHarness(t, nil, nil)
	if got.List != kanban.Backlog || !slicesContains(got.Labels, "build") || got.Attempts != 1 {
		t.Fatalf("back to the builders: %s %v attempts %d", got.List, got.Labels, got.Attempts)
	}
	if !strings.Contains(got.LastNote(), "manual gate G2 is not met") {
		t.Fatalf("note %q", got.LastNote())
	}
}

func TestReviewerCannotOverrideTheHarnessOnARunnableGate(t *testing.T) {
	got, _ := reviewHarness(t, map[string]testrun.Result{"gate:G1": {Status: testrun.Failed, ExitCode: 1, Output: "--- FAIL: TestA\n"}}, decide("G2", "met"))
	if got.List != kanban.Backlog || !strings.Contains(got.LastNote(), "G1 unmet (exit 1: TestA)") {
		t.Fatalf("a reviewer that approves a failing gate is overruled: %s %q", got.List, got.LastNote())
	}
}

func TestAnAbandonedGateBlocksTheCardAsAHandoff(t *testing.T) {
	got, _ := reviewHarness(t, nil, decide("G2", "abandoned"))
	if got.List != kanban.Blocked {
		t.Fatalf("abandonment is never success: %s", got.List)
	}
	if !strings.Contains(got.LastNote(), "HANDOFF REQUIRED: manual gate G2 was abandoned") {
		t.Fatalf("note %q", got.LastNote())
	}
	if strings.Contains(got.LastNote(), "checked") {
		t.Fatalf("the model's evidence stays on the gate, not in the release note: %q", got.LastNote())
	}
}

func TestManualGatesAreDecidedAfreshByEachReview(t *testing.T) {
	store, reg := testEnv(t)
	it, _, _ := store.Add(kanban.ItemInput{Title: "second review", Labels: []string{"needs-review"}, List: kanban.Review, Gates: reviewGates}, kanban.Provenance{})
	// An earlier review met G2 for an earlier version of the branch.
	claimed, err := store.ClaimID(it.ID, kanban.ClaimRequest{Worker: "earlier", Profile: "p", Lists: []kanban.List{kanban.Review}, Lease: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetGate(claimed.ID, "earlier", "G2", kanban.GateMet, "", "old"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Unclaim(it.ID, "earlier", "", false); err != nil {
		t.Fatal(err)
	}
	var seen kanban.GateState
	w := newWorker(t, store, reg, gatedReviewer(), func(ctx context.Context, tt Turn) (run.Result, error) {
		cur, _ := store.Get(it.ID)
		g, _ := cur.GateByID("G2")
		seen = g.State
		return complete(ctx, tt)
	})
	w.Repo = gitRepo(t)
	w.Suites = func(context.Context) []testdetect.Suite { return nil }
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if seen != kanban.GateUnmet {
		t.Fatalf("a met gate from an earlier review is reset before this one starts, saw %q", seen)
	}
}

func TestReviewerGetsKanbanGateOnItsClaim(t *testing.T) {
	var claim *tools.WorkerClaim
	got, _ := reviewHarness(t, nil, func(tt Turn, _ tools.KanbanGate) { claim = tt.Claim })
	_ = got
	if claim == nil || !claim.GateReview {
		t.Fatalf("a worker whose profile reviews gets the tool: %+v", claim)
	}
}

func slicesContains(s []string, v string) bool {
	for _, e := range s {
		if e == v {
			return true
		}
	}
	return false
}
