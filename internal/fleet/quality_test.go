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
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/quality"
	"github.com/vulnetix/belai/internal/testdetect"
	"github.com/vulnetix/belai/internal/testrun"
)

func qualityScout() agentprofile.AgentProfile {
	p := scoutProfile()
	p.Name = "t-quality-scout"
	p.Kanban.Survey = nil
	p.Kanban.Quality = &agentprofile.QualitySpec{Sweep: true}
	return p
}

const failingGo = `?   	example.com/m/cmd	[no test files]
ok  	example.com/m/a	0.010s	coverage: 91.5% of statements
ok  	example.com/m/b	0.012s	coverage: 12.0% of statements
--- FAIL: TestBroken (0.00s)
FAIL	example.com/m/c	0.020s
`

const passingGo = `?   	example.com/m/cmd	[no test files]
ok  	example.com/m/a	0.010s	coverage: 91.5% of statements
ok  	example.com/m/b	0.012s	coverage: 12.0% of statements
ok  	example.com/m/c	0.020s	coverage: 80.0% of statements
`

// stubSuites makes a worker see one Go suite and run it by returning out.
func stubSuites(w *Worker, status testrun.Status, out string, runs *[]testrun.Plan) {
	w.Suites = func(context.Context) []testdetect.Suite {
		return []testdetect.Suite{{Name: "go", Ecosystem: "go", Framework: "go", Command: []string{"go", "test", "./..."}}}
	}
	w.RunTests = func(_ context.Context, plan testrun.Plan) []testrun.Result {
		*runs = append(*runs, plan)
		code := 0
		if status == testrun.Failed {
			code = 1
		}
		return []testrun.Result{{Suite: "go", Command: plan.Argvs[0], Status: status, ExitCode: code, Duration: time.Second, Output: out}}
	}
}

func qualityCards(t *testing.T, store *kanban.Store) []kanban.Item {
	t.Helper()
	all, err := store.Search(kanban.Query{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	var out []kanban.Item
	for _, it := range all {
		if strings.HasPrefix(it.Finding, quality.Prefix) {
			out = append(out, it)
		}
	}
	return out
}

func TestQualitySweepRunsTheSuitesTiesThemToHEADAndSeedsCards(t *testing.T) {
	store, reg := testEnv(t)
	w := newWorker(t, store, reg, qualityScout(), complete)
	w.Head = head(secHeadNew)
	var runs []testrun.Plan
	stubSuites(w, testrun.Failed, failingGo, &runs)
	w.Once = true
	// The scout would claim the seeds it just filed; assigned-only leaves them
	// on the board as the sweep left it.
	w.Profile.Kanban.AssignedOnly = true
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || !slices.Equal(runs[0].Argvs[0], []string{"go", "test", "-cover", "./..."}) {
		t.Fatalf("plans %+v: the Go suite must run with coverage, once", runs)
	}
	rec, ok := quality.ReadRecord(w.Repo, secHeadNew)
	if !ok || !rec.Ran() || len(rec.Failing) != 1 || !rec.CoverageMeasured {
		t.Fatalf("record %+v %v", rec, ok)
	}
	cards := qualityCards(t, store)
	byPrefix := map[string]kanban.Item{}
	for _, c := range cards {
		if c.SeenRef != secHeadNew || c.List != kanban.Backlog || !slices.Contains(c.Labels, agentprofile.QualityLabel) {
			t.Fatalf("card %+v", c)
		}
		for _, p := range []string{quality.PrefixFail, quality.PrefixCover, quality.PrefixUntested, quality.PrefixCategory} {
			if strings.HasPrefix(c.Finding, p) {
				byPrefix[p] = c
			}
		}
	}
	if len(byPrefix) != 4 || byPrefix[quality.PrefixFail].Priority != 3 {
		t.Fatalf("cards %+v", cards)
	}
	if strings.Contains(byPrefix[quality.PrefixFail].Body, "FAIL\t") {
		t.Fatal("raw test output reached a card")
	}
}

func TestQualitySweepDoesNotRunOrDoubleAnything_OnTheSameHEAD(t *testing.T) {
	store, reg := testEnv(t)
	first := newWorker(t, store, reg, qualityScout(), complete)
	first.Head = head(secHeadNew)
	first.Profile.Kanban.AssignedOnly = true
	var runs []testrun.Plan
	stubSuites(first, testrun.Failed, failingGo, &runs)
	if err := first.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := len(qualityCards(t, store))

	second := newWorker(t, store, reg, qualityScout(), complete)
	second.Repo = first.Repo // launched again in the same repository
	second.Head = head(secHeadNew)
	second.Profile.Kanban.AssignedOnly = true
	second.Suites = first.Suites
	second.RunTests = func(context.Context, testrun.Plan) []testrun.Result {
		t.Fatal("the suites ran again on a commit that already has a record")
		return nil
	}
	if err := second.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if after := len(qualityCards(t, store)); after != before {
		t.Fatalf("relaunch changed the board: %d cards, then %d", before, after)
	}
}

func TestQualitySweepOnANewHEADRunsAgainAndClosesWhatIsNoLongerReported(t *testing.T) {
	store, reg := testEnv(t)
	w := newWorker(t, store, reg, qualityScout(), complete)
	w.Profile.Kanban.AssignedOnly = true
	var runs []testrun.Plan
	w.Head = head(secHeadOld)
	stubSuites(w, testrun.Failed, failingGo, &runs)
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := qualityCards(t, store)

	// The test is fixed on the next commit.
	w2 := newWorker(t, store, reg, qualityScout(), complete)
	w2.Repo, w2.Head = w.Repo, head(secHeadNew)
	w2.Profile.Kanban.AssignedOnly = true
	stubSuites(w2, testrun.Passed, passingGo, &runs)
	if err := w2.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("suites ran %d times, want once per HEAD", len(runs))
	}
	var failClosed, categoriesOpen int
	for _, c := range qualityCards(t, store) {
		switch {
		case strings.HasPrefix(c.Finding, quality.PrefixFail) && c.List == kanban.Done:
			failClosed++
		case strings.HasPrefix(c.Finding, quality.PrefixCategory) && c.List == kanban.Backlog:
			categoriesOpen++
		}
	}
	if failClosed != 1 {
		t.Fatalf("the fixed failure's card was not closed: %+v", qualityCards(t, store))
	}
	wantCats := 0
	for _, c := range before {
		if strings.HasPrefix(c.Finding, quality.PrefixCategory) {
			wantCats++
		}
	}
	if categoriesOpen != wantCats {
		t.Fatalf("category cards %d, want the same %d as before", categoriesOpen, wantCats)
	}
}

func TestQualitySweepRecordsNothingWhenNoSuiteRan(t *testing.T) {
	store, reg := testEnv(t)
	w := newWorker(t, store, reg, qualityScout(), complete)
	w.Head = head(secHeadNew)
	w.Profile.Kanban.AssignedOnly = true
	var runs []testrun.Plan
	stubSuites(w, testrun.Denied, "", &runs)
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := quality.ReadRecord(w.Repo, secHeadNew); ok {
		t.Fatal("a run in which nothing ran was recorded as a result for HEAD")
	}
	for _, c := range qualityCards(t, store) {
		if strings.HasPrefix(c.Finding, quality.PrefixFail) {
			t.Fatalf("a denied suite seeded a failure card: %+v", c)
		}
	}
}

func TestQualityCardHandoffsGoToReview(t *testing.T) {
	store, reg := testEnv(t)
	var turns []Turn
	w := newWorker(t, store, reg, qualityScout(), handoffRunner(store, &turns))
	w.Head = head(secHeadNew)
	var runs []testrun.Plan
	stubSuites(w, testrun.Failed, failingGo, &runs)
	// Claim exactly the seeds: labelled scout and quality.
	w.Profile.Kanban.Labels = []string{"scout", "quality"}
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0].Claim.HandoffList != kanban.Review {
		t.Fatalf("turns %d, handoff list %v", len(turns), turns)
	}
	h, _ := store.Get(turns[0].Claim.HandedOff()[0])
	if h.List != kanban.Review {
		t.Fatalf("handoff went to %s, want review whatever the model asked", h.List)
	}
}

func TestWithCoverage(t *testing.T) {
	root := t.TempDir()
	plan := testrun.Plan{Names: []string{"go"}, Argvs: [][]string{{"go", "test", "./..."}}}
	got := withCoverage(plan, nil, root)
	if !slices.Equal(got.Argvs[0], []string{"go", "test", "-cover", "./..."}) || len(got.Argvs) != 1 {
		t.Fatalf("plain go test: %+v", got)
	}
	// A recipe suite in a Go module gets its own coverage run.
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module m\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	just := testrun.Plan{Names: []string{"just-check"}, Argvs: [][]string{{"just", "check"}}}
	got = withCoverage(just, nil, root)
	if len(got.Argvs) != 2 || got.Names[1] != "go-cover" {
		t.Fatalf("just in a go module: %+v", got)
	}
	// No suite at all stays empty, and a non-Go tree gets nothing extra.
	if got := withCoverage(testrun.Plan{}, nil, root); len(got.Argvs) != 0 {
		t.Fatalf("empty plan grew: %+v", got)
	}
	if got := withCoverage(just, nil, t.TempDir()); len(got.Argvs) != 1 {
		t.Fatalf("non-go tree: %+v", got)
	}
}
