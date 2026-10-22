package kanban

import (
	"errors"
	"strings"
	"testing"
)

const prA = "https://github.com/acme/web/pull/148"

// sharpCard files the card a sweep files for one advisory on a package.
func sharpCard(t *testing.T, s *Store, finding, pkg, ref string) Item {
	t.Helper()
	in := FindingInput{
		Finding: finding, Title: "[sca] " + finding + " " + pkg, Priority: 2, Labels: []string{LabelVuln}, Ref: ref,
		Body: "finding: " + finding + "\nkind: sca\npackage: " + pkg + "\necosystem: npm\nversion: 0.35.0-rc.6\n",
	}
	it, _, err := s.UpsertFinding(in, prov)
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func openOnly(urls ...string) func(string) bool {
	return func(u string) bool {
		for _, v := range urls {
			if u == v {
				return true
			}
		}
		return false
	}
}

func TestLaneOfReadsPackageAndEcosystem(t *testing.T) {
	l, ok := LaneOf(Item{Finding: "GHSA-1", Body: "package: Sharp\necosystem: NPM\n"})
	if !ok || l != (Lane{Ecosystem: "npm", Package: "sharp"}) {
		t.Fatalf("lane = %+v %v", l, ok)
	}
	for _, it := range []Item{
		{Body: "package: sharp"},
		{Finding: "GHSA-1", Body: "ecosystem: npm"},
		{Finding: "GHSA-1", Body: "no lines here"},
	} {
		if _, ok := LaneOf(it); ok {
			t.Fatalf("lane for %+v", it)
		}
	}
}

// The incident: a finding's card is done and its pull request is still open
// for review, so the next scan reports the finding again.
func TestUpsertFindingFilesNothingWhileItsPullRequestIsOpen(t *testing.T) {
	s := testStore(t)
	a := sharpCard(t, s, "GHSA-wq5f", "sharp", refOld)
	if _, err := s.SetPR(a.ID, prA, "s"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Move(a.ID, Done, "fixed", "s"); err != nil {
		t.Fatal(err)
	}
	in := findingIn("GHSA-wq5f", refNew)
	in.PROpen = openOnly(prA)
	b, ch, err := s.UpsertFinding(in, prov)
	if err != nil || ch != FindingRefreshed || b.ID != a.ID {
		t.Fatalf("open pull request: %v %s %+v", err, ch, b)
	}
	if items, _ := s.Search(Query{}); len(items) != 1 {
		t.Fatalf("%d cards while the pull request is open", len(items))
	}
	// Closed unmerged and still reported: a new card is right.
	in.PROpen = openOnly()
	c, ch, err := s.UpsertFinding(in, prov)
	if err != nil || ch != FindingReopened || c.ID == a.ID || c.Parent != a.ID {
		t.Fatalf("closed pull request: %v %s %+v", err, ch, c)
	}
}

func TestClaimHoldsACardWhoseFixIsAlreadyOpen(t *testing.T) {
	s := testStore(t)
	a := sharpCard(t, s, "GHSA-wq5f", "sharp", refOld)
	b := sharpCard(t, s, "GHSA-rgj7", "sharp", refOld)
	other := sharpCard(t, s, "GHSA-zzzz", "lodash", refOld)
	if _, err := s.SetPR(a.ID, prA, "s"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Move(a.ID, Done, "fixed", "s"); err != nil {
		t.Fatal(err)
	}
	r := claimReq("w1")
	r.OpenPR = openOnly(prA)

	held, err := s.Held(r.OpenPR)
	if err != nil || len(held) != 1 || held[b.ID].By != a.Short() || held[b.ID].PR != prA || held[b.ID].Why != "same package" {
		t.Fatalf("held = %+v %v", held, err)
	}
	if n, _ := s.Demand(r); n != 1 {
		t.Fatalf("demand %d, want only the other package", n)
	}
	got, err := s.Claim(r)
	if err != nil || got.ID != other.ID {
		t.Fatalf("claimed %+v %v, want the lodash card", got, err)
	}
	if _, err := s.Claim(r); !errors.Is(err, ErrNoWork) {
		t.Fatalf("second claim: %v", err)
	}
	if _, err := s.ClaimID(b.ID, r); !errors.Is(err, ErrNotClaimable) || !strings.Contains(err.Error(), "duplicates") {
		t.Fatalf("ClaimID: %v", err)
	}
	// The pull request merged or was closed: the card is free.
	r.OpenPR = openOnly()
	got, err = s.Claim(r)
	if err != nil || got.ID != b.ID {
		t.Fatalf("after close: %+v %v", got, err)
	}
}

func TestClaimHoldsACardWhileAWorkerIsOnTheSamePackage(t *testing.T) {
	s := testStore(t)
	sharpCard(t, s, "GHSA-wq5f", "sharp", refOld)
	sharpCard(t, s, "GHSA-rgj7", "sharp", refOld)
	// No forge answer at all: the claims alone hold the second card.
	first, err := s.Claim(claimReq("w1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(claimReq("w2")); !errors.Is(err, ErrNoWork) {
		t.Fatalf("second worker took a duplicate: %v", err)
	}
	// The first worker lets go without a pull request: the lane is free again.
	if _, err := s.Unclaim(first.ID, "stopped", "s", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(claimReq("w2")); err != nil {
		t.Fatalf("lane stayed held: %v", err)
	}
}

func TestHoldIndexNamesTheSameFindingBeforeThePackage(t *testing.T) {
	front := Item{ID: "aaaaaaaa-0000", Finding: "GHSA-1", PR: prA, Body: "package: sharp\necosystem: npm"}
	later := Item{ID: "bbbbbbbb-0000", Finding: "GHSA-1", Body: "package: sharp\necosystem: npm"}
	idx := newHoldIndex(&Board{Items: []Item{front, later}}, 0, openOnly(prA))
	h, ok := idx.of(later)
	if !ok || h.Why != "same finding" || h.PR != prA || h.By != front.Short() {
		t.Fatalf("hold = %+v %v", h, ok)
	}
	if _, ok := idx.of(front); ok {
		t.Fatal("a card was held by itself")
	}
	if _, ok := idx.of(Item{ID: "cccccccc-0000", Body: "package: sharp"}); ok {
		t.Fatal("a card with no finding was held")
	}
}

func TestGuardLeavesReviewAndHandFiledCardsAlone(t *testing.T) {
	s := testStore(t)
	a := sharpCard(t, s, "GHSA-wq5f", "sharp", refOld)
	b := sharpCard(t, s, "GHSA-rgj7", "sharp", refOld)
	if _, err := s.SetPR(a.ID, prA, "s"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Move(a.ID, Done, "fixed", "s"); err != nil {
		t.Fatal(err)
	}
	// A card already built and waiting for the verifier is not held back: its
	// work exists, and the verifier decides which pull request stays.
	if _, err := s.Move(b.ID, Review, "built", "s"); err != nil {
		t.Fatal(err)
	}
	hand := addItem(t, s, ItemInput{Title: "update sharp by hand", List: Backlog})
	r := claimReq("w1")
	r.Lists = []List{Backlog, Review}
	r.OpenPR = openOnly(prA)
	got := map[string]bool{}
	for range 2 {
		it, err := s.Claim(r)
		if err != nil {
			t.Fatal(err)
		}
		got[it.ID] = true
	}
	if !got[b.ID] || !got[hand.ID] {
		t.Fatalf("claimed %v, want the review card and the hand-filed one", got)
	}
}
