package fleet

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/headless"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/scanartifacts"
	"github.com/vulnetix/belai/internal/tools"
)

const (
	secHeadOld = "1111111111111111111111111111111111111111"
	secHeadNew = "2222222222222222222222222222222222222222"
)

func sweeper() agentprofile.AgentProfile {
	p := scoutProfile()
	p.Name = "t-vuln-scout"
	p.Tools = []string{"Read", "Vulnetix"}
	p.Kanban.Survey = nil
	p.Kanban.Labels = []string{"vuln-scan"}
	p.Kanban.Security = &agentprofile.SecuritySpec{Sweep: true}
	return p
}

func reconciler() agentprofile.AgentProfile {
	p := scoutProfile()
	p.Name = "t-patcher"
	p.Kanban.Survey = nil
	p.Kanban.Labels = []string{"unrelated"} // claims none of the cards under test
	p.Kanban.Security = &agentprofile.SecuritySpec{Reconcile: true}
	return p
}

// writeReview leaves a memory.yaml for commit with the given open findings.
func writeReview(t *testing.T, repo, commit string, ids ...string) {
	t.Helper()
	dir := filepath.Join(repo, ".vulnetix")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("version: \"1\"\nlast_scan:\n    git_commit: " + commit + "\nfindings:\n")
	for _, id := range ids {
		b.WriteString("    " + id + ":\n        package: lodash\n        ecosystem: npm\n        severity: high\n        status: affected\n        discovery:\n            file: ./package.json\n        versions:\n            current: 4.17.0\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.yaml"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func securityCards(t *testing.T, store *kanban.Store) []kanban.Item {
	t.Helper()
	out, err := store.Search(kanban.Query{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var cards []kanban.Item
	for _, it := range out {
		if it.Finding != "" && !strings.HasPrefix(it.Finding, SweepFindingPrefix) {
			cards = append(cards, it)
		}
	}
	return cards
}

func head(ref string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return ref, nil }
}

func TestSweepRunsTheReviewWhenHEADHasNoEvidenceAndFilesACardPerFinding(t *testing.T) {
	store, reg := testEnv(t)
	w := newWorker(t, store, reg, sweeper(), complete)
	w.Head = head(secHeadNew)
	reviews := 0
	w.Review = func(context.Context) error {
		reviews++
		writeReview(t, w.Repo, secHeadNew, "GHSA-aaaa-bbbb-cccc", "CVE-2026-1")
		return nil
	}
	// An older review is on disk: it is not evidence for HEAD.
	writeReview(t, w.Repo, secHeadOld, "CVE-2026-1")
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reviews != 1 {
		t.Fatalf("review ran %d times, want once", reviews)
	}
	cards := securityCards(t, store)
	if len(cards) != 2 {
		t.Fatalf("%d cards, want one per finding: %+v", len(cards), cards)
	}
	for _, c := range cards {
		if c.SeenRef != secHeadNew || c.List != kanban.Backlog || !slices.Contains(c.Labels, kanban.LabelVuln) || c.Priority != 2 {
			t.Fatalf("card %+v", c)
		}
		if !strings.Contains(c.Body, "package: lodash") || !strings.Contains(c.Body, "file: package.json") {
			t.Fatalf("body %q", c.Body)
		}
	}
}

func TestSweepDoesNotScanWhenHEADIsAlreadyReviewed(t *testing.T) {
	store, reg := testEnv(t)
	w := newWorker(t, store, reg, sweeper(), complete)
	w.Head = head(secHeadNew)
	w.Review = func(context.Context) error {
		t.Fatal("the review ran although an artefact records HEAD")
		return nil
	}
	writeReview(t, w.Repo, secHeadNew, "CVE-2026-2")
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(securityCards(t, store)); n != 1 {
		t.Fatalf("%d cards, want 1", n)
	}
}

func TestSweepFilesNothingWhenTheReviewLeavesNoEvidence(t *testing.T) {
	store, reg := testEnv(t)
	w := newWorker(t, store, reg, sweeper(), complete)
	w.Head = head(secHeadNew)
	w.Review = func(context.Context) error { return os.ErrPermission }
	writeReview(t, w.Repo, secHeadOld, "CVE-2026-3")
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(securityCards(t, store)); n != 0 {
		t.Fatalf("stale artefacts produced %d cards", n)
	}
}

func TestSweepRunsAgainOnANewHEADWithNoClock(t *testing.T) {
	store, reg := testEnv(t)
	w := newWorker(t, store, reg, sweeper(), complete)
	reviews := 0
	w.Review = func(context.Context) error {
		reviews++
		return nil
	}
	for _, h := range []string{secHeadOld, secHeadNew} {
		w.Head = head(h)
		w.sweptRef = ""
		writeReview(t, w.Repo, h, "CVE-2026-4")
		if err := w.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if reviews != 0 {
		t.Fatalf("review ran %d times although each HEAD had evidence", reviews)
	}
	cards := securityCards(t, store)
	if len(cards) != 1 || cards[0].SeenRef != secHeadNew {
		t.Fatalf("cards %+v", cards)
	}
}

func TestReconcileTurnsAVanishedFindingIntoAGoneCard(t *testing.T) {
	store, reg := testEnv(t)
	sw := newWorker(t, store, reg, sweeper(), complete)
	sw.Head = head(secHeadOld)
	writeReview(t, sw.Repo, secHeadOld, "CVE-A", "CVE-B")
	if err := sw.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A later commit no longer reports CVE-A. The patcher reads the artefacts;
	// it does not scan.
	pw := newWorker(t, store, reg, reconciler(), complete)
	pw.Repo = sw.Repo
	pw.Head = head(secHeadNew)
	pw.Review = func(context.Context) error {
		t.Fatal("a reconciling worker must never scan")
		return nil
	}
	writeReview(t, sw.Repo, secHeadNew, "CVE-B")
	if err := pw.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	byFinding := map[string]kanban.Item{}
	for _, c := range securityCards(t, store) {
		byFinding[c.Finding] = c
	}
	a, b := byFinding["CVE-A"], byFinding["CVE-B"]
	if a.List != kanban.Review || a.Verdict != kanban.VerdictFixed || a.SeenRef != secHeadOld ||
		!slices.Contains(a.Labels, kanban.LabelGone) || !slices.Contains(a.Labels, kanban.LabelNeedsVerify) {
		t.Fatalf("gone card %+v", a)
	}
	if b.List != kanban.Backlog || b.Verdict != "" {
		t.Fatalf("present card %+v", b)
	}
}

func TestReconcileWaitsForEvidenceOnHEAD(t *testing.T) {
	store, reg := testEnv(t)
	sw := newWorker(t, store, reg, sweeper(), complete)
	sw.Head = head(secHeadOld)
	writeReview(t, sw.Repo, secHeadOld, "CVE-A")
	if err := sw.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	pw := newWorker(t, store, reg, reconciler(), complete)
	pw.Repo = sw.Repo
	pw.Head = head(secHeadNew) // no artefact for this commit yet
	if err := pw.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c := securityCards(t, store)[0]; c.Verdict != "" || c.List != kanban.Backlog {
		t.Fatalf("card changed without evidence for HEAD: %+v", c)
	}
}

var _ = run.Result{}

// The sweep is keyed on HEAD: polling again on the same commit does not scan
// twice, a failed sweep is not retried until HEAD moves, and a new commit is
// swept again.
func TestSecurityStepSweepsOncePerHEAD(t *testing.T) {
	store, reg := testEnv(t)
	w := newWorker(t, store, reg, sweeper(), complete)
	reviews := 0
	w.Review = func(context.Context) error { reviews++; return nil } // leaves no artefact
	for _, h := range []string{secHeadOld, secHeadOld, secHeadOld, secHeadNew, secHeadNew} {
		w.Head = head(h)
		w.securityStep(context.Background(), "")
	}
	if reviews != 2 {
		t.Fatalf("review ran %d times, want once per HEAD (2)", reviews)
	}
}

func TestSweepFilesOneEnrichmentItemPerCommitOnlyForNewCards(t *testing.T) {
	store, reg := testEnv(t)
	w := newWorker(t, store, reg, sweeper(), complete)
	w.Profile.Kanban.AssignedOnly = true // leave the items on the board
	w.Head = head(secHeadNew)
	writeReview(t, w.Repo, secHeadNew, "CVE-A", "CVE-B")
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	enrich := func() []kanban.Item {
		all, _ := store.Search(kanban.Query{Limit: 100})
		var out []kanban.Item
		for _, it := range all {
			if strings.HasPrefix(it.Finding, SweepFindingPrefix) {
				out = append(out, it)
			}
		}
		return out
	}
	items := enrich()
	if len(items) != 1 || items[0].SeenRef != secHeadNew || !slices.Contains(items[0].Labels, EnrichLabel) || !slices.Contains(items[0].Labels, "vuln-scan") {
		t.Fatalf("enrichment items %+v", items)
	}
	// Sweeping the same commit again finds no new cards and files nothing.
	w.sweptRef = ""
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(enrich()); n != 1 {
		t.Fatalf("%d enrichment items after a second sweep", n)
	}
}

func TestEnrichmentTargetsComeFromBoardFactsOnly(t *testing.T) {
	store, reg := testEnv(t)
	w := newWorker(t, store, reg, sweeper(), complete)
	w.Profile.Kanban.AssignedOnly = true
	w.Head = head(secHeadNew)
	writeReview(t, w.Repo, secHeadNew, "CVE-A", "CVE-B")
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	all, _ := store.Search(kanban.Query{Limit: 100})
	var enrich kanban.Item
	cards := map[string]kanban.Item{}
	for _, it := range all {
		switch {
		case strings.HasPrefix(it.Finding, SweepFindingPrefix):
			enrich = it
		case it.Finding != "":
			cards[it.Finding] = it
		}
	}
	got := w.enrichmentTargets(enrich)
	if len(got) != 2 {
		t.Fatalf("targets %v, want the two new cards", got)
	}
	// A card of an older commit, a hand-filed card and a claimed card are not targets.
	old, _, _ := store.UpsertFinding(findingCard(scanartifacts.ReviewFinding{ID: "CVE-OLD", Kind: scanartifacts.KindSCA, Severity: "low"}, secHeadOld), kanban.ProvenanceFor(w.Repo, "x", headless.HostID()))
	hand, _, _ := store.Add(kanban.ItemInput{Title: "mine", Labels: []string{"vuln"}}, kanban.ProvenanceFor(w.Repo, "x", headless.HostID()))
	// A model that rewrites the enrichment item's text gains nothing.
	body := "K-000000 " + old.ID + " " + hand.ID
	if _, err := store.Update(enrich.ID, kanban.Patch{Body: &body}, "s"); err != nil {
		t.Fatal(err)
	}
	enrich, _ = store.Get(enrich.ID)
	if got := w.enrichmentTargets(enrich); len(got) != 2 || slices.Contains(got, old.ID) || slices.Contains(got, hand.ID) {
		t.Fatalf("targets after edits %v", got)
	}
	if _, err := store.Claim(kanban.ClaimRequest{Worker: "p1", Profile: "patcher", Host: "h", Lease: time.Minute, Labels: []string{"vuln"}}); err != nil {
		t.Fatal(err)
	}
	if got := w.enrichmentTargets(enrich); len(got) >= 2 {
		t.Fatalf("a claimed card is still a target: %v", got)
	}
	// Not an enrichment item: no targets at all.
	if got := w.enrichmentTargets(cards["CVE-A"]); got != nil {
		t.Fatalf("a finding card got targets %v", got)
	}
}

func TestScoutEnrichmentTurnMayNoteTheNewCardsOnly(t *testing.T) {
	store, reg := testEnv(t)
	var noted string
	var refused error
	w := newWorker(t, store, reg, sweeper(), nil)
	w.Head = head(secHeadNew)
	w.Runner = func(ctx context.Context, tt Turn) (run.Result, error) {
		if !strings.HasPrefix(tt.Item.Finding, SweepFindingPrefix) {
			return complete(ctx, tt)
		}
		if len(tt.Claim.Notable) != 2 {
			t.Errorf("claim.Notable = %v", tt.Claim.Notable)
			return complete(ctx, tt)
		}
		up := tools.KanbanUpdate{KanbanBase: tools.KanbanBase{Store: store, Claim: tt.Claim}}
		if _, err := up.Execute(ctx, map[string]any{"id": tt.Claim.Notable[0], "note": "bump to 4.17.21; run npm install"}); err != nil {
			t.Errorf("note on a target: %v", err)
		}
		noted = tt.Claim.Notable[0]
		_, refused = up.Execute(ctx, map[string]any{"id": tt.Claim.Notable[0], "title": "renamed"})
		other, _, _ := store.Add(kanban.ItemInput{Title: "somebody else's card"}, kanban.Provenance{})
		if _, err := up.Execute(ctx, map[string]any{"id": other.ID, "note": "x"}); err == nil {
			t.Error("a note on a card the claim does not name was accepted")
		}
		return complete(ctx, tt)
	}
	writeReview(t, w.Repo, secHeadNew, "CVE-A", "CVE-B")
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if noted == "" || refused == nil {
		t.Fatalf("noted %q, title change refused: %v", noted, refused)
	}
	got, _ := store.Get(noted)
	if !strings.Contains(got.LastNote(), "bump to 4.17.21") || strings.Contains(got.Title, "renamed") {
		t.Fatalf("card %+v", got)
	}
}
