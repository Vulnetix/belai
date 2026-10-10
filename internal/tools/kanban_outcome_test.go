package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/kanban"
)

func outcomeSetup(t *testing.T) (KanbanOutcome, *WorkerClaim, *kanban.Store, kanban.Item) {
	t.Helper()
	store := kanban.Open(filepath.Join(t.TempDir(), "kanban"))
	it, _, err := store.Add(kanban.ItemInput{Title: "card", Labels: []string{"needs-review"}}, kanban.Provenance{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimID(it.ID, kanban.ClaimRequest{Worker: "w1", Profile: "p", Lease: time.Minute}); err != nil {
		t.Fatal(err)
	}
	claim := &WorkerClaim{Worker: "w1", Item: it.ID}
	return KanbanOutcome{KanbanBase{Store: store, Claim: claim}}, claim, store, it
}

func outcomeCall(t KanbanOutcome, outcome, reason string) error {
	_, err := t.Execute(context.Background(), map[string]any{"outcome": outcome, "reason": reason})
	return err
}

func TestKanbanOutcomeRecordsOnTheClaimAndLeavesANote(t *testing.T) {
	tool, claim, store, it := outcomeSetup(t)
	if _, ok := claim.RecordedOutcome(); ok {
		t.Fatal("nothing is recorded before the call")
	}
	if err := outcomeCall(tool, "failure", "\x1b[1mthe flag is\n parsed twice\x1b[0m"); err != nil {
		t.Fatal(err)
	}
	rec, ok := claim.RecordedOutcome()
	if !ok || rec.Outcome != OutcomeFailure || rec.Reason != "the flag is parsed twice" {
		t.Fatalf("recorded %+v %v", rec, ok)
	}
	got, _ := store.Get(it.ID)
	if note := got.LastNote(); note != "outcome failure: the flag is parsed twice" {
		t.Fatalf("note %q", note)
	}
	if got.List != kanban.InProgress || got.ClaimedBy != "w1" {
		t.Fatalf("the tool moves nothing: %+v", got)
	}
	// A later call replaces an earlier one.
	if err := outcomeCall(tool, "success", "fixed in the last commit"); err != nil {
		t.Fatal(err)
	}
	if rec, _ := claim.RecordedOutcome(); rec.Outcome != OutcomeSuccess {
		t.Fatalf("the latest call stands: %+v", rec)
	}
}

func TestKanbanOutcomeRefusesWhatItCannotTake(t *testing.T) {
	tool, claim, _, _ := outcomeSetup(t)
	for name, c := range map[string][2]string{
		"unknown outcome": {"done", "x"},
		"empty outcome":   {"", "x"},
		"no reason":       {"success", " \n "},
	} {
		if err := outcomeCall(tool, c[0], c[1]); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, ok := claim.RecordedOutcome(); ok {
		t.Fatal("a refused call records nothing")
	}
	if _, err := (KanbanOutcome{KanbanBase{Store: tool.Store}}).Execute(context.Background(), map[string]any{"outcome": "success", "reason": "x"}); err == nil || !strings.Contains(err.Error(), "holds a claim") {
		t.Fatalf("a session with no claim has no outcome: %v", err)
	}
}

func TestEveryWorkerClaimGetsKanbanOutcome(t *testing.T) {
	store := kanban.Open(filepath.Join(t.TempDir(), "kanban"))
	reg := NewRegistry().WithKanbanWorker(store, nil, &WorkerClaim{Worker: "w", Item: "i"})
	if _, ok := reg.Find(KanbanOutcomeName); !ok {
		t.Fatal("a worker with a claim has KanbanOutcome")
	}
	if _, ok := NewRegistry().WithKanbanWorker(store, nil, nil).Find(KanbanOutcomeName); ok {
		t.Fatal("a session with no claim must not")
	}
}

func TestRecordedOutcomeOnANilClaim(t *testing.T) {
	var c *WorkerClaim
	if _, ok := c.RecordedOutcome(); ok {
		t.Fatal("a nil claim records nothing")
	}
}
