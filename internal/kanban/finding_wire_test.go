package kanban

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/sessionsync"
)

const goodVEX = ".vulnetix/vex/CVE-2026-9.openvex.json"

func securityCard(t *testing.T, s *Store) Item {
	t.Helper()
	a, _, err := s.UpsertFinding(findingIn("CVE-2026-9", refOld), prov)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// pulled builds what another host or the website sent for a card: the same id,
// newer, carrying the given security facts.
func pulled(it Item, f func(a *sessionsync.KanbanAgent)) Item {
	w := ToWire(it)
	w.UpdatedAt = it.Updated + 1000
	w.Version = it.ServerVersion + 1
	f(w.Agent)
	return FromWire(w)
}

func TestSecurityFieldsRideTheWire(t *testing.T) {
	it := Item{ID: "a1", Title: "t", List: Review, Finding: "CVE-2026-9", SeenRef: refOld, Verdict: VerdictFixed, VEX: goodVEX}
	w := ToWire(it)
	if a := w.Agent; a.Finding != "CVE-2026-9" || a.SeenRef != refOld || a.Verdict != "fixed" || a.VEX != goodVEX {
		t.Fatalf("wire agent = %+v", a)
	}
	back := FromWire(w)
	if back.Finding != it.Finding || back.SeenRef != it.SeenRef || back.Verdict != it.Verdict || back.VEX != it.VEX {
		t.Fatalf("round trip = %+v", back)
	}
	// An ordinary card sends none of them.
	plain := ToWire(Item{ID: "a2", Title: "t", List: Backlog})
	if a := plain.Agent; a.Finding != "" || a.SeenRef != "" || a.Verdict != "" || a.VEX != "" {
		t.Fatalf("an ordinary card sent security facts: %+v", a)
	}
}

func TestAPulledCardBringsItsFindingToAHostThatHasNone(t *testing.T) {
	s := testStore(t)
	// Another host filed the card; this host has never seen it.
	remote := Item{ID: "77777777-7777-4777-8777-777777777777", Title: "[vuln] CVE-2026-9 lodash", List: Backlog,
		Finding: "CVE-2026-9", SeenRef: refOld, Verdict: VerdictNoFix, VEX: goodVEX, remoteAgent: true,
		Project: "belai", ProjectKey: "belai-1234", Labels: []string{LabelVuln}, Updated: 5000, ServerVersion: 3}
	if _, err := s.Merge([]Item{remote}, 3); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(remote.ID)
	if err != nil || got.Finding != "CVE-2026-9" || got.SeenRef != refOld || got.Verdict != VerdictNoFix || got.VEX != goodVEX {
		t.Fatalf("pulled card = %+v err=%v", got, err)
	}
	// This host's own scan now finds that card instead of filing a second one.
	c, ch, err := s.UpsertFinding(findingIn("CVE-2026-9", refNew), prov)
	if err != nil || ch != FindingRefreshed || c.ID != remote.ID {
		t.Fatalf("scan after the pull: %v %s %+v", err, ch, c)
	}
	if items, _ := s.Search(Query{}); len(items) != 1 {
		t.Fatalf("%d cards for one finding", len(items))
	}
}

func TestAPulledCopyNeverReplacesOrClearsALocalFinding(t *testing.T) {
	s := testStore(t)
	a := securityCard(t, s)
	local, _ := s.Get(a.ID)

	// A newer pull that wins last-writer-wins, with other values.
	other := pulled(local, func(ag *sessionsync.KanbanAgent) {
		ag.Finding, ag.SeenRef, ag.Verdict, ag.VEX = "GHSA-other", refNew, "rejected", ".vulnetix/vex/GHSA-other.openvex.json"
	})
	other.Dirty = false
	if _, err := s.Merge([]Item{other}, 9); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(a.ID)
	if got.Finding != "CVE-2026-9" || got.SeenRef != refOld {
		t.Fatalf("a pulled copy replaced the local finding: %+v", got)
	}
	if got.Verdict != VerdictRejected {
		// The local verdict was empty, so the pulled one fills it.
		t.Fatalf("verdict = %q, want the pulled one into an empty field", got.Verdict)
	}

	// A newer pull that carries none of them clears nothing.
	bare := pulled(got, func(ag *sessionsync.KanbanAgent) {
		ag.Finding, ag.SeenRef, ag.Verdict, ag.VEX = "", "", "", ""
	})
	bare.Updated += 1000
	bare.ServerVersion += 1
	if _, err := s.Merge([]Item{bare}, 10); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Get(a.ID)
	if after.Finding != "CVE-2026-9" || after.SeenRef != refOld || after.Verdict != VerdictRejected {
		t.Fatalf("a pulled copy without them cleared the local facts: %+v", after)
	}
}

func TestAPulledFindingFillsAnEmptyFieldEvenWhenTheLocalCopyWins(t *testing.T) {
	s := testStore(t)
	it, _, err := s.Add(ItemInput{Title: "an ordinary card"}, prov)
	if err != nil {
		t.Fatal(err)
	}
	// The local copy has an unpushed edit, so it wins last-writer-wins.
	if _, err := s.Update(it.ID, Patch{Note: "local edit"}, "s"); err != nil {
		t.Fatal(err)
	}
	local, _ := s.Get(it.ID)
	r := pulled(local, func(ag *sessionsync.KanbanAgent) { ag.Finding, ag.SeenRef = "CVE-2026-9", refOld })
	r.Updated = local.Updated - 10 // older than the local edit
	if _, err := s.Merge([]Item{r}, 2); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(it.ID)
	if got.Finding != "CVE-2026-9" || got.SeenRef != refOld {
		t.Fatalf("an empty field was not filled: %+v", got)
	}
	if !got.Dirty || !strings.Contains(got.LastNote(), "local edit") {
		t.Fatalf("the local edit must survive: %+v", got)
	}
}

func TestPulledSecurityFactsKeepTheHarnessShapeOrGo(t *testing.T) {
	bad := map[string]func(a *sessionsync.KanbanAgent){
		"finding with a space":  func(a *sessionsync.KanbanAgent) { a.Finding = "CVE 2026 9" },
		"finding too long":      func(a *sessionsync.KanbanAgent) { a.Finding = strings.Repeat("a", 65) },
		"finding with markup":   func(a *sessionsync.KanbanAgent) { a.Finding = "<b>CVE</b>" },
		"short commit":          func(a *sessionsync.KanbanAgent) { a.SeenRef = "abc123" },
		"not a verdict":         func(a *sessionsync.KanbanAgent) { a.Verdict = "ship it" },
		"vex path escapes":      func(a *sessionsync.KanbanAgent) { a.VEX = ".vulnetix/vex/../../../etc/passwd" },
		"vex absolute path":     func(a *sessionsync.KanbanAgent) { a.VEX = "/etc/passwd" },
		"vex wrong directory":   func(a *sessionsync.KanbanAgent) { a.VEX = "docs/CVE-1.openvex.json" },
		"vex wrong extension":   func(a *sessionsync.KanbanAgent) { a.VEX = ".vulnetix/vex/CVE-1.sh" },
		"vex with a newline":    func(a *sessionsync.KanbanAgent) { a.VEX = ".vulnetix/vex/a\nb.openvex.json" },
		"vex with a quote":      func(a *sessionsync.KanbanAgent) { a.VEX = `.vulnetix/vex/a"b.openvex.json` },
		"vex name starts a dir": func(a *sessionsync.KanbanAgent) { a.VEX = ".vulnetix/vex/a/b.openvex.json" },
	}
	for name, f := range bad {
		s := testStore(t)
		it := Item{ID: "88888888-8888-4888-8888-888888888888", Title: "t", List: Backlog, Updated: 100, ServerVersion: 1}
		r := pulled(it, f)
		if _, err := s.Merge([]Item{r}, 1); err != nil {
			t.Fatal(err)
		}
		got, err := s.Get(it.ID)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.Finding != "" || got.SeenRef != "" || got.Verdict != "" || got.VEX != "" {
			t.Errorf("%s: kept %+v", name, got)
		}
	}
}
