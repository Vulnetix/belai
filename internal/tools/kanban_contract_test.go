package tools

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/kanban"
)

func coverageSetup(t *testing.T) (KanbanContract, KanbanHandoff, *WorkerClaim, *kanban.Store, kanban.Item) {
	t.Helper()
	store := kanban.Open(filepath.Join(t.TempDir(), "kanban"))
	it, _, err := store.Add(kanban.ItemInput{Title: "request: add export and import"}, kanban.Provenance{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimID(it.ID, kanban.ClaimRequest{Worker: "w1", Profile: "p", Lease: time.Minute}); err != nil {
		t.Fatal(err)
	}
	claim := &WorkerClaim{Worker: "w1", Item: it.ID, HandoffLabels: []string{"build"}, Coverage: true}
	base := KanbanBase{Store: store, Claim: claim}
	return KanbanContract{base}, KanbanHandoff{base}, claim, store, it
}

func TestContractThenHandoffsCoverTheClauses(t *testing.T) {
	contract, handoff, claim, store, it := coverageSetup(t)
	ctx := context.Background()
	if _, err := contract.Execute(ctx, map[string]any{"clauses": []any{"export to CSV", "import from CSV"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := handoff.Execute(ctx, map[string]any{"title": "build export", "labels": []any{"build"}, "covers": []any{"C1"}}); err != nil {
		t.Fatal(err)
	}
	child, _ := store.Get(claim.HandedOff()[0])
	if len(child.Covers) != 1 || child.Covers[0] != "C1" {
		t.Fatalf("covers %v", child.Covers)
	}
	parent, _ := store.Get(it.ID)
	kids, _ := store.Children(it.ID)
	if gaps := parent.CoverageGaps(kids); len(gaps) != 1 || gaps[0] != "C2" {
		t.Fatalf("gaps %v", gaps)
	}
}

func TestHandoffRefusedUntilClausesAreRecordedAndCovered(t *testing.T) {
	contract, handoff, claim, _, _ := coverageSetup(t)
	ctx := context.Background()
	_, err := handoff.Execute(ctx, map[string]any{"title": "early", "labels": []any{"build"}, "covers": []any{"C1"}})
	if err == nil || !strings.Contains(err.Error(), "record the request's clauses") {
		t.Fatalf("a handoff before the clauses: %v", err)
	}
	if _, err := contract.Execute(ctx, map[string]any{"clauses": []any{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	for name, covers := range map[string]any{"none": nil, "empty": []any{}, "unknown": []any{"C7"}, "malformed": []any{"../C1"}} {
		args := map[string]any{"title": "t " + name, "labels": []any{"build"}}
		if covers != nil {
			args["covers"] = covers
		}
		if _, err := handoff.Execute(ctx, args); err == nil {
			t.Errorf("%s: a handoff must cover at least one real clause", name)
		}
	}
	if len(claim.HandedOff()) != 0 {
		t.Fatal("refused handoffs file nothing")
	}
}

func TestContractIsRefusedOnceAHandoffExists(t *testing.T) {
	contract, handoff, _, _, _ := coverageSetup(t)
	ctx := context.Background()
	contract.Execute(ctx, map[string]any{"clauses": []any{"a"}})
	if _, err := handoff.Execute(ctx, map[string]any{"title": "t", "labels": []any{"build"}, "covers": []any{"C1"}}); err != nil {
		t.Fatal(err)
	}
	_, err := contract.Execute(ctx, map[string]any{"clauses": []any{"a", "b"}})
	if err == nil || !strings.Contains(err.Error(), "before the first handoff") {
		t.Fatalf("re-planning under existing handoffs would orphan their covers: %v", err)
	}
}

func TestContractIsBoundToItsClaim(t *testing.T) {
	contract, _, claim, store, it := coverageSetup(t)
	ctx := context.Background()
	claim.Coverage = false
	if _, err := contract.Execute(ctx, map[string]any{"clauses": []any{"a"}}); err == nil {
		t.Fatal("a worker that does not plan requests recorded clauses")
	}
	claim.Coverage = true
	if _, err := contract.Execute(ctx, map[string]any{"clauses": "not a list"}); err != nil {
		// A comma string is accepted like every list argument.
		t.Fatalf("string list: %v", err)
	}
	if _, err := contract.Execute(ctx, map[string]any{"clauses": []any{}}); err == nil {
		t.Fatal("an empty inventory is refused")
	}
	if _, err := store.Unclaim(it.ID, "taken", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := contract.Execute(ctx, map[string]any{"clauses": []any{"a"}}); err == nil {
		t.Fatal("clauses were recorded without holding the claim")
	}
	if _, err := (KanbanContract{KanbanBase{Store: store}}).Execute(ctx, map[string]any{"clauses": []any{"a"}}); err == nil {
		t.Fatal("no claim, no contract")
	}
}

func TestContractAndCoversOnlyOnTheSurfaceOfAPlanner(t *testing.T) {
	store := kanban.Open(filepath.Join(t.TempDir(), "kanban"))
	names := func(claim *WorkerClaim) []string {
		var out []string
		for _, d := range NewRegistry().WithKanbanWorker(store, nil, claim).Definitions() {
			out = append(out, d.Name)
		}
		return out
	}
	if slices.Contains(names(&WorkerClaim{Worker: "w", Item: "i"}), KanbanContractName) {
		t.Fatal("a worker that does not plan got KanbanContract")
	}
	if !slices.Contains(names(&WorkerClaim{Worker: "w", Item: "i", Coverage: true}), KanbanContractName) || !IsCoreTool(KanbanContractName) {
		t.Fatal("a planner has KanbanContract, advertised in full")
	}
	_, handoff, claim, _, _ := coverageSetup(t)
	if _, ok := handoff.Definition().Properties["covers"]; !ok {
		t.Fatal("a planner's handoff takes covers")
	}
	claim.Coverage = false
	if _, ok := handoff.Definition().Properties["covers"]; ok {
		t.Fatal("no covers argument without coverage")
	}
}
