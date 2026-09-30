package kanban

import (
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	refOld = "1111111111111111111111111111111111111111"
	refNew = "2222222222222222222222222222222222222222"
)

func findingIn(id, ref string) FindingInput {
	return FindingInput{Finding: id, Title: "[vuln] " + id + " lodash", Body: "manifest: package.json", Priority: 2, Labels: []string{LabelVuln}, Ref: ref}
}

func TestUpsertFindingCreatesOnceAndRefreshes(t *testing.T) {
	s := testStore(t)
	a, ch, err := s.UpsertFinding(findingIn("GHSA-aaaa-bbbb-cccc", refOld), prov)
	if err != nil || ch != FindingCreated {
		t.Fatalf("first upsert: %v %s", err, ch)
	}
	if a.Finding != "GHSA-aaaa-bbbb-cccc" || a.SeenRef != refOld || a.List != Backlog || !slices.Contains(a.Labels, LabelVuln) {
		t.Fatalf("card = %+v", a)
	}
	b, ch, err := s.UpsertFinding(findingIn("GHSA-aaaa-bbbb-cccc", refNew), prov)
	if err != nil || ch != FindingRefreshed || b.ID != a.ID || b.SeenRef != refNew {
		t.Fatalf("second upsert: %v %s %+v", err, ch, b)
	}
	items, _ := s.Search(Query{})
	if len(items) != 1 {
		t.Fatalf("%d cards for one finding", len(items))
	}
}

func TestUpsertFindingRejectsBadInput(t *testing.T) {
	s := testStore(t)
	for _, in := range []FindingInput{
		{Finding: "has space", Title: "t", Ref: refOld},
		{Finding: "GHSA-1", Title: "t", Ref: "abc"},
		{Finding: "GHSA-1", Title: "", Ref: refOld},
		{Finding: strings.Repeat("a", 65), Title: "t", Ref: refOld},
	} {
		if _, _, err := s.UpsertFinding(in, prov); err == nil {
			t.Fatalf("accepted %+v", in)
		}
	}
}

func TestUpsertFindingReopensAfterDone(t *testing.T) {
	s := testStore(t)
	a, _, _ := s.UpsertFinding(findingIn("CVE-2026-1", refOld), prov)
	if _, err := s.Move(a.ID, Done, "fixed", "s"); err != nil {
		t.Fatal(err)
	}
	b, ch, err := s.UpsertFinding(findingIn("CVE-2026-1", refNew), prov)
	if err != nil || ch != FindingReopened || b.ID == a.ID || b.Parent != a.ID {
		t.Fatalf("reopen: %v %s %+v", err, ch, b)
	}
}

func TestReconcileMarksAbsentFindingsGone(t *testing.T) {
	s := testStore(t)
	gone, _, _ := s.UpsertFinding(findingIn("CVE-1", refOld), prov)
	kept, _, _ := s.UpsertFinding(findingIn("CVE-2", refOld), prov)
	same, _, _ := s.UpsertFinding(findingIn("CVE-3", refNew), prov) // seen on this ref already
	blocked, _, _ := s.UpsertFinding(findingIn("CVE-4", refOld), prov)
	_, _ = s.Move(blocked.ID, Blocked, "", "s")

	changed, err := s.Reconcile(prov, map[string]bool{"CVE-2": true}, refNew, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0].ID != gone.ID {
		t.Fatalf("changed = %+v", changed)
	}
	g, _ := s.Get(gone.ID)
	if g.List != Review || g.Verdict != VerdictFixed || g.SeenRef != refOld ||
		!slices.Contains(g.Labels, LabelGone) || !slices.Contains(g.Labels, LabelNeedsVerify) || slices.Contains(g.Labels, LabelVuln) {
		t.Fatalf("gone card = %+v", g)
	}
	for _, id := range []string{kept.ID, same.ID, blocked.ID} {
		it, _ := s.Get(id)
		if it.Verdict != "" || slices.Contains(it.Labels, LabelGone) {
			t.Fatalf("card %s changed: %+v", id, it)
		}
	}
	// A second pass finds nothing left to do.
	if again, err := s.Reconcile(prov, map[string]bool{"CVE-2": true}, refNew, nil); err != nil || len(again) != 0 {
		t.Fatalf("second reconcile changed %d: %v", len(again), err)
	}
}

func TestReconcileLeavesClaimedCards(t *testing.T) {
	s := testStore(t)
	a, _, _ := s.UpsertFinding(findingIn("CVE-9", refOld), prov)
	if _, err := s.Claim(ClaimRequest{Worker: "w1", Profile: "patcher", Project: prov.Project, Host: "h", Lease: 5 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	if changed, _ := s.Reconcile(prov, map[string]bool{}, refNew, nil); len(changed) != 0 {
		t.Fatalf("a claimed card was reconciled: %+v", changed)
	}
	it, _ := s.Get(a.ID)
	if it.Verdict != "" {
		t.Fatal("verdict set on a claimed card")
	}
}

func TestUpsertFindingPullsAGoneCardBack(t *testing.T) {
	s := testStore(t)
	a, _, _ := s.UpsertFinding(findingIn("CVE-5", refOld), prov)
	_, _ = s.Reconcile(prov, map[string]bool{}, refNew, nil)
	b, ch, err := s.UpsertFinding(findingIn("CVE-5", refNew), prov)
	if err != nil || ch != FindingReopened || b.ID != a.ID || b.List != Backlog || b.Verdict != "" ||
		slices.Contains(b.Labels, LabelGone) || !slices.Contains(b.Labels, LabelVuln) {
		t.Fatalf("pull back: %v %s %+v", err, ch, b)
	}
}

func TestFindingFieldsSurviveAPull(t *testing.T) {
	local := Item{Finding: "CVE-6", SeenRef: refOld, Verdict: VerdictNoFix, VEX: "vex/CVE-6.openvex.json"}
	for _, remoteAgent := range []bool{false, true} {
		r := Item{remoteAgent: remoteAgent}
		keepAgent(&r, local)
		if r.Finding != local.Finding || r.SeenRef != local.SeenRef || r.Verdict != local.Verdict || r.VEX != local.VEX {
			t.Fatalf("remoteAgent=%v cleared the finding fields: %+v", remoteAgent, r)
		}
	}
}

func TestUpsertFindingConcurrentWritersMakeOneCard(t *testing.T) {
	s := testStore(t)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _ = s.UpsertFinding(findingIn("CVE-7", refOld), prov)
		}()
	}
	wg.Wait()
	items, _ := s.Search(Query{})
	if len(items) != 1 {
		t.Fatalf("%d cards after concurrent upserts", len(items))
	}
}

func TestReconcileSkipsUncoveredKinds(t *testing.T) {
	s := testStore(t)
	sast, _, _ := s.UpsertFinding(findingIn("sast:rule:abcd1234", refOld), prov)
	sca, _, _ := s.UpsertFinding(findingIn("CVE-8", refOld), prov)
	onlySCA := func(id string) bool { return !strings.HasPrefix(id, "sast:") }
	changed, err := s.Reconcile(prov, map[string]bool{}, refNew, onlySCA)
	if err != nil || len(changed) != 1 || changed[0].ID != sca.ID {
		t.Fatalf("changed = %+v, err %v", changed, err)
	}
	if it, _ := s.Get(sast.ID); it.Verdict != "" {
		t.Fatal("a finding of a kind the scan did not cover was marked gone")
	}
}

func onceIn(id, title string) FindingInput {
	in := findingIn(id, refOld)
	in.Title, in.Once = title, true
	return in
}

func TestOnceNeverReopensAFinishedCardOrDoublesAPersonsCard(t *testing.T) {
	s := testStore(t)
	a, ch, err := s.UpsertFinding(onceIn("quality:fail:go:aaaa", "[quality] go suite fails"), prov)
	if err != nil || ch != FindingCreated {
		t.Fatalf("first: %v %s", err, ch)
	}
	if _, err := s.Move(a.ID, Done, "handed off", "s"); err != nil {
		t.Fatal(err)
	}
	b, ch, err := s.UpsertFinding(onceIn("quality:fail:go:aaaa", "[quality] go suite fails"), prov)
	if err != nil || ch != FindingRefreshed || b.ID != a.ID {
		t.Fatalf("a finished card was reopened: %v %s %+v", err, ch, b)
	}
	if items, _ := s.Search(Query{}); len(items) != 1 {
		t.Fatalf("%d cards", len(items))
	}
	// A person already filed the same work under the same title.
	mine, _, _ := s.Add(ItemInput{Title: "[Quality] mutation testing"}, prov)
	c, ch, err := s.UpsertFinding(onceIn("quality:cat:mutation:bbbb", "[quality] mutation testing"), prov)
	if err != nil || ch != FindingRefreshed || c.ID != mine.ID {
		t.Fatalf("a person's card was doubled: %v %s %+v", err, ch, c)
	}
	if items, _ := s.Search(Query{}); len(items) != 2 {
		t.Fatalf("%d cards, want 2", len(items))
	}
}

func TestCloseAbsentClosesOnlyGoneUnclaimedCardsOfThePrefix(t *testing.T) {
	s := testStore(t)
	gone, _, _ := s.UpsertFinding(onceIn("quality:fail:go:1111", "[quality] a"), prov)
	kept, _, _ := s.UpsertFinding(onceIn("quality:fail:go:2222", "[quality] b"), prov)
	other, _, _ := s.UpsertFinding(findingIn("CVE-1", refOld), prov)
	unc, _, _ := s.UpsertFinding(onceIn("quality:cover:3333", "[quality] c"), prov)
	closed, err := s.CloseAbsent(prov, "quality:", map[string]bool{"quality:fail:go:2222": true}, refNew,
		func(id string) bool { return !strings.HasPrefix(id, "quality:cover:") })
	if err != nil || len(closed) != 1 || closed[0].ID != gone.ID {
		t.Fatalf("closed %+v, err %v", closed, err)
	}
	if it, _ := s.Get(gone.ID); it.List != Done || !strings.Contains(it.LastNote(), "no longer reported") {
		t.Fatalf("gone card %+v", it)
	}
	for _, id := range []string{kept.ID, other.ID, unc.ID} {
		if it, _ := s.Get(id); it.List != Backlog {
			t.Fatalf("card %s moved: %+v", id, it)
		}
	}
	if again, _ := s.CloseAbsent(prov, "quality:", nil, refNew, nil); len(again) != 2 {
		t.Fatalf("second pass closed %d, want the two remaining", len(again))
	}
}

func TestOnceRespectsADeletedCard(t *testing.T) {
	s := testStore(t)
	a, _, _ := s.UpsertFinding(onceIn("quality:cat:docs:1", "[quality] docs"), prov)
	if _, err := s.Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.UpsertFinding(onceIn("quality:cat:docs:1", "[quality] docs"), prov); err != nil {
		t.Fatal(err)
	}
	if items, _ := s.Search(Query{}); len(items) != 0 {
		t.Fatalf("a deleted seed card came back: %+v", items)
	}
}
