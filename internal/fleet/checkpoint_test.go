package fleet

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/run"
)

func plannerProfile() agentprofile.AgentProfile {
	p := builderProfile()
	p.Name = "t-planner"
	p.Kanban.Labels = []string{"plan"}
	p.Kanban.OnSuccess = agentprofile.Route{
		List: "review", Labels: []string{"build"}, DropLabels: []string{"plan"},
		Checkpoint: kanban.CheckpointPlanApproval,
	}
	return p
}

// A stage whose route names a checkpoint hands its card on held: the next stage
// does not take it until a person approves, and takes it afterwards.
func TestACheckpointHoldsTheCardUntilAPersonApproves(t *testing.T) {
	store, reg := testEnv(t)
	it, _, _ := store.Add(kanban.ItemInput{Title: "add the export", Labels: []string{"plan"}}, kanban.Provenance{})
	planner := newWorker(t, store, reg, plannerProfile(), complete)
	if err := planner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	held, _ := store.Get(it.ID)
	if held.List != kanban.Review || !slices.Contains(held.Labels, kanban.CheckpointLabel(kanban.CheckpointPlanApproval)) || !slices.Contains(held.Labels, "build") {
		t.Fatalf("released %s %q", held.List, held.Labels)
	}
	if !strings.Contains(held.LastNote(), "held for a person at checkpoint plan_approval") {
		t.Fatalf("note %q", held.LastNote())
	}

	build := builderProfile()
	build.Name = "t-builder-2"
	build.Kanban.Lists = []string{"review"}
	var turns int
	builder := newWorker(t, store, reg, build, func(ctx context.Context, tt Turn) (run.Result, error) {
		turns++
		return complete(ctx, tt)
	})
	if err := builder.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if turns != 0 {
		t.Fatalf("a worker took a held card (%d turns)", turns)
	}

	if _, err := store.Decide(it.ID, kanban.Decision{Approve: true, Note: "plan is right"}, ""); err != nil {
		t.Fatal(err)
	}
	builder = newWorker(t, store, reg, build, func(ctx context.Context, tt Turn) (run.Result, error) {
		turns++
		return complete(ctx, tt)
	})
	if err := builder.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if turns != 1 {
		t.Fatalf("an approved card ran %d turns, want 1", turns)
	}
}

// A failed attempt is retried and then blocked, never held: only success asks
// a person.
func TestAFailedStageDoesNotHoldTheCard(t *testing.T) {
	store, reg := testEnv(t)
	it, _, _ := store.Add(kanban.ItemInput{Title: "add the export", Labels: []string{"plan"}}, kanban.Provenance{})
	planner := newWorker(t, store, reg, plannerProfile(), func(ctx context.Context, tt Turn) (run.Result, error) {
		return run.Result{}, context.DeadlineExceeded
	})
	_ = planner.Run(context.Background())
	got, _ := store.Get(it.ID)
	if _, held := kanban.HeldFor(got); held {
		t.Fatalf("a failed stage held the card: %q", got.Labels)
	}
}
