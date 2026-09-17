package goals

import (
	"strings"
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

// LatestGoalState must skip entries it cannot parse rather than stopping at
// them: one corrupt line must not hide an earlier good state, and it must not
// be reported as the latest either.
func TestLatestGoalStateSkipsUnparsableEntries(t *testing.T) {
	good := NewGoalState("real goal")
	good.Passes = 7
	entries := []session.Entry{
		good.ToEntry(""),
		{ID: "x", Type: EntryTypeGoalState, Content: "{not json"},
	}
	got, ok := LatestGoalState(entries)
	if !ok {
		t.Fatal("a corrupt trailing entry must not hide the good one")
	}
	if got.Objective != "real goal" || got.Passes != 7 {
		t.Fatalf("LatestGoalState = %+v", got)
	}
}

// Only goal_state entries count; a session full of other entry types reports
// no goal state rather than guessing.
func TestLatestGoalStateIgnoresOtherEntryTypes(t *testing.T) {
	entries := []session.Entry{
		{ID: "a", Type: "message", Content: `{"objective":"not a goal"}`},
		{ID: "b", Type: "plan_state", Content: `{"objective":"also not"}`},
	}
	if _, ok := LatestGoalState(entries); ok {
		t.Fatal("non-goal_state entries must not be read as goal state")
	}
	if _, ok := LatestGoalState(nil); ok {
		t.Fatal("no entries must report no goal state")
	}
}

// Every emit is its own entry with its own id and a fresh updatedAt, so the
// session log keeps the whole history of a goal rather than one mutable row.
func TestToEntryStampsFreshIDAndTimestamp(t *testing.T) {
	gs := NewGoalState("ship it")
	gs.UpdatedAt = 0

	first := gs.ToEntry("parent")
	second := gs.ToEntry("parent")
	if first.ID == second.ID {
		t.Fatal("each emitted entry must carry its own id")
	}
	if first.ParentID != "parent" || second.ParentID != "parent" {
		t.Fatalf("parent id not carried: %q %q", first.ParentID, second.ParentID)
	}

	parsed, err := GoalStateFromEntry(first)
	if err != nil {
		t.Fatalf("GoalStateFromEntry: %v", err)
	}
	if parsed.UpdatedAt == 0 {
		t.Fatal("ToEntry must stamp updatedAt even when the caller left it zero")
	}
	// The receiver is a value, so stamping must not leak back to the caller's
	// copy — the loop keeps one GoalState across every pass.
	if gs.UpdatedAt != 0 {
		t.Fatal("ToEntry must not mutate the caller's GoalState")
	}
}

// A fresh state is active, version 1, and unbounded.
func TestNewGoalStateDefaults(t *testing.T) {
	gs := NewGoalState("objective text")
	if gs.Version != 1 {
		t.Fatalf("version = %d, want 1", gs.Version)
	}
	if gs.Status != string(StatusActive) {
		t.Fatalf("status = %q, want active", gs.Status)
	}
	if gs.TokenBudget != nil {
		t.Fatal("a fresh goal must be unbounded (nil token budget)")
	}
	if gs.ID == "" {
		t.Fatal("a fresh goal must carry an id")
	}
	if gs.CreatedAt == 0 || gs.UpdatedAt == 0 {
		t.Fatalf("timestamps not stamped: %+v", gs)
	}
	if gs.Passes != 0 || gs.TokensUsed != 0 || gs.TimeUsedSeconds != 0 {
		t.Fatalf("counters must start at zero: %+v", gs)
	}
}

// A nil token budget means unbounded and must stay out of the JSON, so a
// reader cannot mistake an absent budget for a zero one.
func TestGoalStateOmitsNilTokenBudget(t *testing.T) {
	e := NewGoalState("x").ToEntry("")
	if strings.Contains(e.Content, "tokenBudget") {
		t.Fatalf("nil token budget must be omitted from the JSON: %s", e.Content)
	}
	budget := 5000
	gs := NewGoalState("x")
	gs.TokenBudget = &budget
	e = gs.ToEntry("")
	if !strings.Contains(e.Content, `"tokenBudget":5000`) {
		t.Fatalf("a set token budget must serialise: %s", e.Content)
	}
}

// A malformed entry reports an error rather than a zero state that would read
// as a real goal.
func TestGoalStateFromEntryRejectsMalformed(t *testing.T) {
	if _, err := GoalStateFromEntry(session.Entry{Type: EntryTypeGoalState, Content: "nope"}); err == nil {
		t.Fatal("malformed content must error")
	}
}
