package goals

import (
	"testing"

	"github.com/vulnetix/signet/internal/session"
)

func TestGoalStateRoundTrip(t *testing.T) {
	gs := NewGoalState("ship the thing")
	gs.Passes = 3
	gs.TokensUsed = 1234

	e := gs.ToEntry("parent-id")
	if e.Type != EntryTypeGoalState || e.ID == "" {
		t.Fatalf("entry = %+v", e)
	}
	got, err := GoalStateFromEntry(e)
	if err != nil {
		t.Fatalf("GoalStateFromEntry: %v", err)
	}
	if got.Objective != "ship the thing" || got.Passes != 3 || got.TokensUsed != 1234 || got.Version != 1 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestLatestGoalStatePicksLast(t *testing.T) {
	a := NewGoalState("first")
	a.Passes = 1
	b := NewGoalState("second")
	b.Passes = 2
	entries := []session.Entry{a.ToEntry(""), b.ToEntry("")}
	got, ok := LatestGoalState(entries)
	if !ok || got.Objective != "second" || got.Passes != 2 {
		t.Fatalf("LatestGoalState = %+v, %v", got, ok)
	}
}
