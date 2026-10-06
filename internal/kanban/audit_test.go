package kanban

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/audit"
)

// auditRecorder installs a Recorder for the test and returns a reader of what it
// wrote so far.
func auditRecorder(t *testing.T) func() []audit.Event {
	t.Helper()
	rec, err := audit.Open(t.TempDir(), "box")
	if err != nil {
		t.Fatal(err)
	}
	audit.SetDefault(rec)
	t.Cleanup(func() { audit.SetDefault(nil); rec.Close() })
	return func() []audit.Event {
		f, err := os.Open(rec.Path())
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		var out []audit.Event
		sc := bufio.NewScanner(f)
		sc.Buffer(nil, 2<<20)
		for sc.Scan() {
			var e audit.Event
			if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
				t.Fatal(err)
			}
			out = append(out, e)
		}
		if at := audit.Verify(out); at != -1 {
			t.Fatalf("the stream's chain breaks at %d", at)
		}
		return out
	}
}

// A claim is the worker's to record (it knows its profile, crew and repository),
// so the store writes nothing for one, and card text never reaches the log.
func TestTheStoreLeavesClaimsToTheWorker(t *testing.T) {
	events := auditRecorder(t)
	s := testStore(t)
	if _, err := s.Claim(claimReq("w")); err == nil {
		t.Fatal("expected ErrNoWork")
	}
	addItem(t, s, ItemInput{Title: "build the thing", Body: "SECRET BODY TEXT", Labels: []string{"build"}})
	r := claimReq("worker-1")
	r.Labels = []string{"build"}
	if _, err := s.Claim(r); err != nil {
		t.Fatal(err)
	}
	if evs := events(); len(evs) != 0 {
		t.Fatalf("the store recorded %d events for claims: %+v", len(evs), evs)
	}
}

func TestLapsedLeaseIsAudited(t *testing.T) {
	events := auditRecorder(t)
	s := testStore(t)
	clock := time.UnixMilli(2_000_000)
	s.now = func() time.Time { return clock }
	it := addItem(t, s, ItemInput{Title: "slow", Labels: []string{"build"}})
	r := claimReq("worker-9")
	r.Labels = []string{"build"}
	if _, err := s.Claim(r); err != nil {
		t.Fatal(err)
	}

	clock = clock.Add(time.Hour) // the worker never renewed
	n, err := s.Reap()
	if err != nil || n != 1 {
		t.Fatalf("reap = %d, %v", n, err)
	}
	evs := events()
	if len(evs) != 1 {
		t.Fatalf("%d events, want the lapse: %+v", len(evs), evs)
	}
	e := evs[0]
	if e.Kind != "card.lease_lapsed" || e.ActorKind != "harness" || e.ItemID != it.ID || e.Data["worker"] != "worker-9" ||
		e.Data["list"] != "backlog" || e.Data["attempt"] != "1" || e.Outcome != "lapsed" || e.Repo != "belai" {
		t.Fatalf("lapse event = %+v", e)
	}
	// Nothing left to reap, so nothing more is written.
	if n, _ := s.Reap(); n != 0 || len(events()) != 1 {
		t.Fatalf("a second reap added events: %d", len(events()))
	}
}

func TestFindingCardLinksToTheVulnerability(t *testing.T) {
	events := auditRecorder(t)
	s := testStore(t)

	a, ch, err := s.UpsertFinding(findingIn("CVE-2026-4242", refOld), prov)
	if err != nil || ch != FindingCreated {
		t.Fatal(err, ch)
	}
	// A refresh of the same card writes nothing new.
	if _, ch, _ = s.UpsertFinding(findingIn("CVE-2026-4242", refOld), prov); ch != FindingRefreshed {
		t.Fatalf("change = %s", ch)
	}
	// The finding is gone from the next scan: the card is reconciled.
	gone, err := s.Reconcile(prov, map[string]bool{}, refNew, nil)
	if err != nil || len(gone) != 1 {
		t.Fatalf("reconcile = %v %v", gone, err)
	}

	evs := events()
	if len(evs) != 2 {
		t.Fatalf("%d events, want carded and reconciled: %+v", len(evs), evs)
	}
	carded, reconciled := evs[0], evs[1]
	if carded.Kind != "finding.carded" || carded.VulnID != "CVE-2026-4242" || carded.ItemID != a.ID ||
		carded.SeenRef != refOld || carded.ActorKind != "harness" || carded.Data["change"] != "created" {
		t.Fatalf("carded = %+v", carded)
	}
	if reconciled.Kind != "finding.reconciled" || reconciled.VulnID != "CVE-2026-4242" || reconciled.Verdict != "fixed" ||
		reconciled.Data["change"] != "gone" {
		t.Fatalf("reconciled = %+v", reconciled)
	}
}

func TestQualityFindingsAreNotVulnerabilityWork(t *testing.T) {
	events := auditRecorder(t)
	s := testStore(t)
	// A failing-test card carries a finding id and no vuln label.
	in := FindingInput{Finding: "test:go:pkg.TestThing", Title: "failing test TestThing", Body: "x", Ref: refOld}
	if _, ch, err := s.UpsertFinding(in, prov); err != nil || ch != FindingCreated {
		t.Fatal(err, ch)
	}
	evs := events()
	if len(evs) != 1 || evs[0].Kind != "finding.carded" {
		t.Fatalf("events = %+v", evs)
	}
	if evs[0].VulnID != "" || evs[0].SeenRef != "" {
		t.Fatalf("a quality card linked to a vulnerability: %+v", evs[0])
	}
}

func TestVulnIDOnlyForSecurityCards(t *testing.T) {
	cases := []struct {
		name string
		it   Item
		want string
	}{
		{"no finding", Item{Labels: []string{LabelVuln}}, ""},
		{"vuln label", Item{Finding: "CVE-1", Labels: []string{LabelVuln}}, "CVE-1"},
		{"gone after reconcile", Item{Finding: "CVE-1", Labels: []string{LabelGone, LabelNeedsVerify}}, "CVE-1"},
		{"verdict recorded", Item{Finding: "CVE-1", Verdict: VerdictFixed}, "CVE-1"},
		{"vex written", Item{Finding: "CVE-1", VEX: ".vulnetix/vex/CVE-1.openvex.json"}, "CVE-1"},
		{"quality card", Item{Finding: "test:go:x", Labels: []string{"quality"}}, ""},
	}
	for _, c := range cases {
		if got := c.it.VulnID(); got != c.want {
			t.Errorf("%s: VulnID = %q, want %q", c.name, got, c.want)
		}
	}
}
