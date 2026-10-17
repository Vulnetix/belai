package tools

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/vulnetix/belai/internal/kanban"
)

// A claim with HandoffCheckpoint holds every handoff for a person, and the
// model can neither name the checkpoint nor escape it.
func TestHandoffIsHeldAtTheProfilesCheckpoint(t *testing.T) {
	store := kanban.Open(filepath.Join(t.TempDir(), "kanban"))
	parent, _, _ := store.Add(kanban.ItemInput{Title: "plan the export"}, kanban.Provenance{})
	claim := &WorkerClaim{
		Worker: "w1", Item: parent.ID, HandoffLabels: []string{"build"},
		HandoffCheckpoint: kanban.CheckpointPlanApproval,
	}
	h := KanbanHandoff{KanbanBase{Store: store, Claim: claim}}
	if _, err := h.Execute(context.Background(), map[string]any{"title": "add the csv writer", "labels": []any{"build"}}); err != nil {
		t.Fatal(err)
	}
	it, _ := store.Get(claim.HandedOff()[0])
	if reason, held := kanban.HeldFor(it); !held || reason != kanban.CheckpointPlanApproval || !slices.Contains(it.Labels, "build") {
		t.Fatalf("handoff labels %q", it.Labels)
	}

	// The model asking for a checkpoint label, or for none, changes nothing: the
	// label is not on the profile's handoff list, so it is refused.
	_, err := h.Execute(context.Background(), map[string]any{"title": "another", "labels": []any{kanban.CheckpointLabel(kanban.CheckpointVisual)}})
	if err == nil {
		t.Fatal("the model named a checkpoint label")
	}

	free := &WorkerClaim{Worker: "w1", Item: parent.ID, HandoffLabels: []string{"build"}}
	if _, err := (KanbanHandoff{KanbanBase{Store: store, Claim: free}}).Execute(context.Background(), map[string]any{"title": "unheld", "labels": []any{"build"}}); err != nil {
		t.Fatal(err)
	}
	it, _ = store.Get(free.HandedOff()[0])
	if _, held := kanban.HeldFor(it); held {
		t.Fatalf("a profile with no checkpoint held a handoff: %q", it.Labels)
	}
}
