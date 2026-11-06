package tools

import (
	"context"
	"strings"
	"testing"
)

// A candidate attempt is one of several independent attempts at a card, so
// nothing it writes to the board may reach another candidate: every tool that
// would append its text to the card is refused, and the card keeps its notes.
func TestCandidateClaimRefusesBoardWrites(t *testing.T) {
	tool, claim, store, it := outcomeSetup(t)
	claim.Candidate = true
	claim.HandoffLabels = []string{"build"}
	claim.Verdicts = []string{"fixed"}
	claim.Coverage = true
	claim.GateReview = true
	base := tool.KanbanBase
	calls := map[string]func() error{
		KanbanUpdateName: func() error {
			_, err := KanbanUpdate{base}.Execute(context.Background(), map[string]any{"id": it.ID, "note": "leaked hint"})
			return err
		},
		KanbanHandoffName: func() error {
			_, err := KanbanHandoff{KanbanBase: base}.Execute(context.Background(), map[string]any{"title": "follow up", "body": "text"})
			return err
		},
		KanbanVerdictName: func() error {
			_, err := KanbanVerdict{base}.Execute(context.Background(), map[string]any{"verdict": "fixed", "justification": "done"})
			return err
		},
		KanbanOutcomeName: func() error { return outcomeCall(tool, "success", "done") },
		KanbanContractName: func() error {
			_, err := KanbanContract{base}.Execute(context.Background(), map[string]any{"clauses": []any{"one"}})
			return err
		},
		KanbanGateName: func() error {
			_, err := KanbanGate{base}.Execute(context.Background(), map[string]any{"gate": "G1", "state": "met"})
			return err
		},
	}
	for name, call := range calls {
		err := call()
		if err == nil || !strings.Contains(err.Error(), "candidate attempt") || !strings.Contains(err.Error(), name) {
			t.Errorf("%s in a candidate attempt: %v", name, err)
		}
	}
	got, _ := store.Get(it.ID)
	if got.LastNote() != "claimed by agent p" {
		t.Fatalf("a candidate left a note: %q", got.LastNote())
	}
	if _, ok := claim.RecordedOutcome(); ok {
		t.Fatal("a candidate recorded an outcome")
	}
}

// The same tools work for an ordinary claim.
func TestOrdinaryClaimIsNotRefusedAsACandidate(t *testing.T) {
	tool, claim, _, it := outcomeSetup(t)
	if claim.Candidate {
		t.Fatal("Candidate must default to false")
	}
	if _, err := (KanbanUpdate{tool.KanbanBase}).Execute(context.Background(), map[string]any{"id": it.ID, "note": "progress"}); err != nil {
		t.Fatal(err)
	}
}
