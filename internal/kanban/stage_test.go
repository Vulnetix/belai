package kanban

import (
	"testing"
	"time"
)

func claimFrom(t *testing.T, s *Store, id, worker string, list List) {
	t.Helper()
	got, err := s.Claim(ClaimRequest{Worker: worker, Profile: "p", Lease: time.Minute, Lists: []List{list}})
	if err != nil || got.ID != id {
		t.Fatalf("claim from %s: %v", list, err)
	}
}

func releaseTo(t *testing.T, s *Store, id, worker string, to List, failed bool) Item {
	t.Helper()
	it, err := s.Release(id, worker, Outcome{To: to, Failed: failed, Note: "n"})
	if err != nil {
		t.Fatal(err)
	}
	return it
}

// Attempts are counted per stage. A builder's tries are not charged to the
// reviewer after it, and a card sent back starts that stage afresh, so the loop
// between the two stages is bounded by Bounces instead.
func TestAttemptsAreCountedPerStage(t *testing.T) {
	s := testStore(t)
	it := addItem(t, s, ItemInput{Title: "work"})

	// Two failed tries at the first stage count as attempts there.
	claimFrom(t, s, it.ID, "builder", Backlog)
	if got := releaseTo(t, s, it.ID, "builder", Backlog, true); got.Attempts != 1 || got.Bounces != 0 {
		t.Fatalf("a retry of the same stage counts an attempt: attempts %d bounces %d", got.Attempts, got.Bounces)
	}
	claimFrom(t, s, it.ID, "builder", Backlog)
	if got := releaseTo(t, s, it.ID, "builder", Backlog, true); got.Attempts != 2 {
		t.Fatalf("attempts %d", got.Attempts)
	}

	// Succeeding into the next stage starts that stage's attempts at zero.
	claimFrom(t, s, it.ID, "builder", Backlog)
	if got := releaseTo(t, s, it.ID, "builder", Review, false); got.Attempts != 0 || got.Bounces != 0 {
		t.Fatalf("moving on resets the attempts: attempts %d bounces %d", got.Attempts, got.Bounces)
	}

	// A rejection sends it back: a bounce, not an attempt, and the stage it
	// returns to starts afresh.
	claimFrom(t, s, it.ID, "reviewer", Review)
	got := releaseTo(t, s, it.ID, "reviewer", Backlog, true)
	if got.Bounces != 1 || got.Attempts != 0 || got.List != Backlog {
		t.Fatalf("a rejection is a bounce: attempts %d bounces %d list %s", got.Attempts, got.Bounces, got.List)
	}

	// The builder's tries after that are its own, and the bounce stays.
	claimFrom(t, s, it.ID, "builder", Backlog)
	if got := releaseTo(t, s, it.ID, "builder", Backlog, true); got.Attempts != 1 || got.Bounces != 1 {
		t.Fatalf("attempts %d bounces %d", got.Attempts, got.Bounces)
	}
}

// A reviewer that fails and keeps the item in review is retrying its own stage.
func TestFailingInPlaceIsAnAttemptNotABounce(t *testing.T) {
	s := testStore(t)
	it := addItem(t, s, ItemInput{Title: "work", List: Review})
	claimFrom(t, s, it.ID, "reviewer", Review)
	if got := releaseTo(t, s, it.ID, "reviewer", Review, true); got.Attempts != 1 || got.Bounces != 0 {
		t.Fatalf("attempts %d bounces %d", got.Attempts, got.Bounces)
	}
}

// Work that is not a step between stages never touches the counters it did not
// earn: a lapsed claim is an attempt at its own stage, and success that stays
// where it was neither resets nor bounces.
func TestStageAccountingLeavesOtherMovesAlone(t *testing.T) {
	s := testStore(t)
	clock := time.UnixMilli(10_000_000)
	s.now = func() time.Time { return clock }
	it := addItem(t, s, ItemInput{Title: "work", List: Review})
	claimFrom(t, s, it.ID, "w1", Review)
	clock = clock.Add(2 * time.Minute)
	if n, _ := s.Reap(); n != 1 {
		t.Fatalf("reaped %d", n)
	}
	if got, _ := s.Get(it.ID); got.Attempts != 1 || got.Bounces != 0 || got.List != Review {
		t.Fatalf("a lapsed claim is an attempt at its stage: %+v", got)
	}

	// A failure into blocked is an attempt, not a bounce.
	claimFrom(t, s, it.ID, "w2", Review)
	if got := releaseTo(t, s, it.ID, "w2", Blocked, true); got.Attempts != 2 || got.Bounces != 0 {
		t.Fatalf("attempts %d bounces %d", got.Attempts, got.Bounces)
	}
}

func TestStageRankAndMovesBack(t *testing.T) {
	for _, c := range []struct {
		from, to List
		back     bool
	}{
		{Review, Backlog, true},
		{Done, Review, true},
		{Done, Backlog, true},
		{Backlog, Review, false},
		{Review, Review, false},
		{Review, Blocked, false},
		{InProgress, Backlog, false},
		{Blocked, Backlog, false},
	} {
		if got := MovesBack(c.from, c.to); got != c.back {
			t.Errorf("MovesBack(%s, %s) = %v, want %v", c.from, c.to, got, c.back)
		}
	}
}
