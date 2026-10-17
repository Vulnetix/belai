package kanban

import (
	"errors"
	"slices"
	"testing"
)

// claimThenRelease claims the one card the worker may take and releases it with
// o, the way a stage hands a card on.
func claimThenRelease(t *testing.T, s *Store, o Outcome) Item {
	t.Helper()
	r := claimReq("w1")
	r.Lists = []List{Backlog, Review}
	got, err := s.Claim(r)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	o.Releaser = "builder"
	out, err := s.Release(got.ID, "w1", o)
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	return out
}

func TestCheckpointLabelHoldsTheCardFromEveryWorker(t *testing.T) {
	s := testStore(t)
	addItem(t, s, ItemInput{Title: "plan the change", Labels: []string{"plan"}})
	held := claimThenRelease(t, s, Outcome{To: Review, AddLabels: []string{CheckpointLabel(CheckpointPlanApproval), "build"}})

	if reason, ok := HeldFor(held); !ok || reason != CheckpointPlanApproval {
		t.Fatalf("held for %q %v, labels %q", reason, ok, held.Labels)
	}
	r := claimReq("w2")
	r.Lists = []List{Backlog, Review}
	if _, err := s.Claim(r); !errors.Is(err, ErrNoWork) {
		t.Fatalf("a held card was claimed: %v", err)
	}
	if _, err := s.ClaimID(held.ID, r); !errors.Is(err, ErrNotClaimable) {
		t.Fatalf("a held card was claimed by id: %v", err)
	}
}

func TestAWorkerOutcomeCannotDropACheckpoint(t *testing.T) {
	// A held card cannot be claimed, so no stage normally releases one; this is
	// the second line: whatever a worker asks to drop, a checkpoint stays.
	got := keepCheckpoints([]string{"repro", CheckpointLabel(CheckpointRootCause), "build"})
	if !slices.Equal(got, []string{"repro", "build"}) {
		t.Fatalf("keepCheckpoints = %q", got)
	}

	s := testStore(t)
	addItem(t, s, ItemInput{Title: "reproduce", Labels: []string{"repro"}})
	held := claimThenRelease(t, s, Outcome{
		To:         Review,
		AddLabels:  []string{CheckpointLabel(CheckpointRootCause)},
		DropLabels: []string{CheckpointLabel(CheckpointRootCause)},
	})
	if _, ok := HeldFor(held); !ok {
		t.Fatalf("an outcome that adds and drops the checkpoint left the card free: %q", held.Labels)
	}
}

func TestDecideApproveClearsTheCheckpointAndKeepsTheList(t *testing.T) {
	s := testStore(t)
	addItem(t, s, ItemInput{Title: "plan", Labels: []string{"plan"}})
	held := claimThenRelease(t, s, Outcome{To: Review, AddLabels: []string{CheckpointLabel(CheckpointPlanApproval), "build"}})

	got, err := s.Decide(held.ID, Decision{Approve: true, Note: "scope is right"}, "sess")
	if err != nil {
		t.Fatal(err)
	}
	if got.List != Review || slices.ContainsFunc(got.Labels, func(l string) bool {
		return len(l) > len(CheckpointPrefix) && l[:len(CheckpointPrefix)] == CheckpointPrefix
	}) {
		t.Fatalf("after approve: list %s labels %q", got.List, got.Labels)
	}
	if !slices.Contains(got.Labels, "build") {
		t.Fatalf("approve dropped a routing label: %q", got.Labels)
	}
	last := got.History[len(got.History)-1]
	if last.Note != "checkpoint plan_approval approved: scope is right" {
		t.Fatalf("history note %q", last.Note)
	}
	r := claimReq("w2")
	r.Lists = []List{Review}
	r.Labels = []string{"build"}
	if _, err := s.Claim(r); err != nil {
		t.Fatalf("the next stage cannot take an approved card: %v", err)
	}
}

func TestDecideRejectSendsTheCardBackAndCountsABounce(t *testing.T) {
	s := testStore(t)
	addItem(t, s, ItemInput{Title: "plan", Labels: []string{"plan"}})
	held := claimThenRelease(t, s, Outcome{To: Review, AddLabels: []string{CheckpointLabel(CheckpointPrePR)}})

	got, err := s.Decide(held.ID, Decision{Back: Backlog, Note: "tests missing"}, "sess")
	if err != nil {
		t.Fatal(err)
	}
	if got.List != Backlog || got.Bounces != held.Bounces+1 || got.Attempts != 0 {
		t.Fatalf("after reject: list %s bounces %d attempts %d", got.List, got.Bounces, got.Attempts)
	}
	if _, held := HeldFor(got); held {
		t.Fatalf("a rejected card is still held: %q", got.Labels)
	}
	if _, err := s.Decide(held.ID, Decision{Approve: true}, "sess"); !errors.Is(err, ErrNotHeld) {
		t.Fatalf("a second decision: %v", err)
	}
}

// A planner's handoff keeps the label a builder claims by, so a rejection to
// blocked is the one that cannot be built; only a deliberate re-queue counts as
// a bounce.
func TestRejectToBlockedKeepsTheRefusedCardOutOfEveryWorkersReach(t *testing.T) {
	s := testStore(t)
	addItem(t, s, ItemInput{Title: "plan", Labels: []string{"plan"}})
	held := claimThenRelease(t, s, Outcome{To: Backlog, AddLabels: []string{CheckpointLabel(CheckpointPlanApproval), "build"}})

	got, err := s.Decide(held.ID, Decision{Back: Blocked, Note: "wrong approach"}, "sess")
	if err != nil {
		t.Fatal(err)
	}
	if got.List != Blocked || got.Bounces != 0 || !slices.Contains(got.Labels, "build") {
		t.Fatalf("after reject: %s bounces %d labels %q", got.List, got.Bounces, got.Labels)
	}
	r := claimReq("w2")
	r.Lists = []List{Backlog, Review}
	r.Labels = []string{"build"}
	if _, err := s.Claim(r); !errors.Is(err, ErrNoWork) {
		t.Fatalf("a rejected plan is claimable: %v", err)
	}
}

func TestDecideNeedsAListToRejectToAndAnUnclaimedCard(t *testing.T) {
	s := testStore(t)
	it := addItem(t, s, ItemInput{Title: "plan", Labels: []string{CheckpointLabel(CheckpointVisual)}})
	if _, err := s.Decide(it.ID, Decision{}, ""); !errors.Is(err, ErrBadList) {
		t.Fatalf("reject without a list: %v", err)
	}
	if _, err := s.Decide("no-such-card", Decision{Approve: true}, ""); err == nil {
		t.Fatal("decided a card that does not exist")
	}
}

func TestEveryCheckpointReasonIsALabelAWorkerCanNotMistake(t *testing.T) {
	for _, r := range CheckpointReasons {
		if !ValidCheckpoint(r) {
			t.Errorf("%q is not valid", r)
		}
		if got := NormLabels([]string{CheckpointLabel(r)}); len(got) != 1 || got[0] != CheckpointLabel(r) {
			t.Errorf("%q does not survive label normalisation: %q", r, got)
		}
	}
	if ValidCheckpoint("anything") {
		t.Error("an unknown reason is valid")
	}
	if _, ok := HeldFor(Item{Labels: []string{"checkpoint:from_a_newer_belai"}}); !ok {
		t.Error("a checkpoint this Belai does not know must still hold the card")
	}
}
