package fleet

import (
	"context"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
)

func planner() agentprofile.AgentProfile {
	p := builderProfile()
	p.Name = "t-scout"
	p.Kanban.Labels = []string{"scout"}
	p.Kanban.HandoffLabels = []string{"build"}
	p.Kanban.OnSuccess = agentprofile.Route{List: "done", DropLabels: []string{"scout"}}
	p.Kanban.Gates = &agentprofile.GatesSpec{Coverage: true}
	return p
}

// plan runs a scout on card labels; script is its turn.
func plan(t *testing.T, labels []string, script func(ctx context.Context, tt Turn, contract tools.KanbanContract, handoff tools.KanbanHandoff)) (kanban.Item, *kanban.Store, *Worker) {
	t.Helper()
	store, reg := testEnv(t)
	it, _, err := store.Add(kanban.ItemInput{Title: "request: export and import", Labels: labels}, kanban.Provenance{})
	if err != nil {
		t.Fatal(err)
	}
	w := newWorker(t, store, reg, planner(), func(ctx context.Context, tt Turn) (run.Result, error) {
		base := tools.KanbanBase{Store: store, Claim: tt.Claim}
		if script != nil {
			script(ctx, tt, tools.KanbanContract{KanbanBase: base}, tools.KanbanHandoff{KanbanBase: base})
		}
		return complete(ctx, tt)
	})
	w.Repo = gitRepo(t)
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(it.ID)
	return got, store, w
}

func gapCards(t *testing.T, store *kanban.Store) []kanban.Item {
	t.Helper()
	all, _ := store.Search(kanban.Query{Limit: 200})
	var out []kanban.Item
	for _, it := range all {
		if strings.HasPrefix(it.Finding, "coverage:") {
			out = append(out, it)
		}
	}
	return out
}

func TestUncoveredClauseGetsAGapCardFiledByTheHarness(t *testing.T) {
	got, store, _ := plan(t, []string{"scout"}, func(ctx context.Context, _ Turn, c tools.KanbanContract, h tools.KanbanHandoff) {
		c.Execute(ctx, map[string]any{"clauses": []any{"export to CSV", "SECRET-TEXT import from CSV"}})
		h.Execute(ctx, map[string]any{"title": "build export", "labels": []any{"build"}, "covers": []any{"C1"}})
	})
	if got.List != kanban.Done || !strings.Contains(got.LastNote(), "coverage: 1 of 2 clauses covered, 1 gap card(s) filed") {
		t.Fatalf("the scout's card completes with the gap filed: %s %q", got.List, got.LastNote())
	}
	gaps := gapCards(t, store)
	if len(gaps) != 1 || !strings.HasSuffix(gaps[0].Finding, ":C2") {
		t.Fatalf("one gap card for C2: %+v", gaps)
	}
	g := gaps[0]
	if strings.Contains(g.Title+g.Body, "SECRET-TEXT") || strings.Contains(g.Title+g.Body, "import from") {
		t.Fatalf("a gap card names ids, never the model's clause text: %q %q", g.Title, g.Body)
	}
	if len(g.Labels) != 1 || g.Labels[0] != CoverageLabel || g.Assignee != "" {
		t.Fatalf("no worker claims it: %v %q", g.Labels, g.Assignee)
	}
}

func TestFullyCoveredRequestFilesNoGaps(t *testing.T) {
	got, store, _ := plan(t, []string{"scout"}, func(ctx context.Context, _ Turn, c tools.KanbanContract, h tools.KanbanHandoff) {
		c.Execute(ctx, map[string]any{"clauses": []any{"a", "b"}})
		h.Execute(ctx, map[string]any{"title": "one", "labels": []any{"build"}, "covers": []any{"C1", "C2"}})
	})
	if got.List != kanban.Done || len(gapCards(t, store)) != 0 || !strings.Contains(got.LastNote(), "coverage: 2 of 2 clauses covered") {
		t.Fatalf("%s %q gaps %d", got.List, got.LastNote(), len(gapCards(t, store)))
	}
}

func TestRequestWithNoRecordedClausesIsAFailedAttempt(t *testing.T) {
	got, _, _ := plan(t, []string{"scout"}, nil)
	if got.List != kanban.Backlog || got.Attempts != 1 || !strings.Contains(got.LastNote(), "recorded no clauses") {
		t.Fatalf("a request that was never planned is not done: %s attempts %d %q", got.List, got.Attempts, got.LastNote())
	}
}

func TestSeededAndSurveyCardsAreNotHeldToCoverage(t *testing.T) {
	for _, label := range []string{agentprofile.QualityLabel, agentprofile.SurveyLabel} {
		got, store, _ := plan(t, []string{"scout", label}, nil)
		if got.List != kanban.Done || len(gapCards(t, store)) != 0 {
			t.Errorf("%s card: %s %q", label, got.List, got.LastNote())
		}
	}
}

func TestGapCardsAreFiledOnceAndNeverDoubled(t *testing.T) {
	script := func(ctx context.Context, _ Turn, c tools.KanbanContract, h tools.KanbanHandoff) {
		c.Execute(ctx, map[string]any{"clauses": []any{"a", "b"}})
		h.Execute(ctx, map[string]any{"title": "one", "labels": []any{"build"}, "covers": []any{"C1"}})
	}
	_, store, w := plan(t, []string{"scout"}, script)
	// The same card worked again (a retry, or a crew relaunch) files no second gap.
	all, _ := store.Search(kanban.Query{Limit: 50})
	var parent kanban.Item
	for _, c := range all {
		if c.Title == "request: export and import" {
			parent = c
		}
	}
	w.reconcileCoverage(context.Background(), outcome{}, parent)
	w.reconcileCoverage(context.Background(), outcome{}, parent)
	if n := len(gapCards(t, store)); n != 1 {
		t.Fatalf("%d gap cards, want one", n)
	}
}

func TestWorkerWithoutCoverageKeepsTheHandoffAsItWas(t *testing.T) {
	store, reg := testEnv(t)
	store.Add(kanban.ItemInput{Title: "request", Labels: []string{"scout"}}, kanban.Provenance{})
	p := planner()
	p.Kanban.Gates = nil
	var got Turn
	w := newWorker(t, store, reg, p, func(ctx context.Context, tt Turn) (run.Result, error) { got = tt; return complete(ctx, tt) })
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got.Claim.Coverage {
		t.Fatal("no gates block, no coverage")
	}
}

func TestPlannerClaimHasCoverageOnARequestOnly(t *testing.T) {
	_, _, _ = plan(t, []string{"scout"}, func(ctx context.Context, tt Turn, _ tools.KanbanContract, _ tools.KanbanHandoff) {
		if !tt.Claim.Coverage {
			t.Error("a request card is planned under coverage")
		}
	})
	_, _, _ = plan(t, []string{"scout", agentprofile.QualityLabel}, func(ctx context.Context, tt Turn, _ tools.KanbanContract, _ tools.KanbanHandoff) {
		if tt.Claim.Coverage {
			t.Error("a seeded card is not held to coverage")
		}
	})
}
