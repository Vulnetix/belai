package fleet

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/rolemanager/jev"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
)

// backend answers every question with one probability, or with none.
type backend struct {
	mu     sync.Mutex
	answer float64
	silent bool
	asked  []decisions.Request
}

func (b *backend) Decide(_ context.Context, r decisions.Request) (decisions.Result, error) {
	b.mu.Lock()
	b.asked = append(b.asked, r)
	b.mu.Unlock()
	out := map[string]decisions.Answer{}
	if !b.silent {
		for id := range r.Questions {
			out[id] = decisions.Answer{Type: decisions.TypeNoul, Noul: b.answer}
		}
	}
	return decisions.Result{Answers: out}, nil
}
func (b *backend) Identity() string           { return "fake/decision" }
func (b *backend) Backend() decisions.Backend { return decisions.BackendSystemOne }

func jobsWith(b *backend, off ...config.JevJob) *jev.Jobs {
	return &jev.Jobs{Client: jev.NewWith(b), On: func(j config.JevJob) bool {
		for _, o := range off {
			if o == j {
				return false
			}
		}
		return true
	}}
}

// coveredRequest plans a two-clause request whose tasks cover both, then runs
// the reconcile with the given backend.
func coveredRequest(t *testing.T, jobs *jev.Jobs) ([]kanban.Item, *Worker) {
	t.Helper()
	_, store, w := plan(t, []string{"scout"}, func(ctx context.Context, _ Turn, c tools.KanbanContract, h tools.KanbanHandoff) {
		c.Execute(ctx, map[string]any{"clauses": []any{"export to CSV", "import from CSV"}})
		h.Execute(ctx, map[string]any{"title": "build export", "labels": []any{"build"}, "covers": []any{"C1"}})
		h.Execute(ctx, map[string]any{"title": "build import", "labels": []any{"build"}, "covers": []any{"C2"}})
	})
	all, _ := store.Search(kanban.Query{Limit: 50})
	var parent kanban.Item
	for _, it := range all {
		if it.Title == "request: export and import" {
			parent = it
		}
	}
	w.Jev = jobs
	w.jevOnce.Do(func() {})
	w.Profile.Kanban.Gates.Coverage = true
	w.reconcileCoverage(context.Background(), outcome{}, parent)
	return gapCards(t, store), w
}

func TestJevDoubtedCoverageFilesAGapCardButNeverUnmarksCoverage(t *testing.T) {
	b := &backend{answer: 0.05}
	gaps, _ := coveredRequest(t, jobsWith(b))
	if len(gaps) != 2 {
		t.Fatalf("both clauses are doubted, so two cards: %d", len(gaps))
	}
	for _, g := range gaps {
		if !strings.Contains(g.Title, "may not be done by its tasks") {
			t.Errorf("title %q", g.Title)
		}
		if strings.Contains(g.Title+g.Body, "export to CSV") || strings.Contains(g.Title+g.Body, "build export") {
			t.Errorf("a card the harness files holds ids only, never the model's words: %q %q", g.Title, g.Body)
		}
	}
}

func TestJevSoundCoverageFilesNothing(t *testing.T) {
	gaps, _ := coveredRequest(t, jobsWith(&backend{answer: 0.95}))
	if len(gaps) != 0 {
		t.Fatalf("well-rated coverage files nothing: %d", len(gaps))
	}
}

func TestJevUnknownOrOffLeavesTheRecordedCoverageAlone(t *testing.T) {
	if gaps, _ := coveredRequest(t, jobsWith(&backend{silent: true})); len(gaps) != 0 {
		t.Fatalf("an unanswered rating is not a low one: %d", len(gaps))
	}
	if gaps, _ := coveredRequest(t, jobsWith(&backend{answer: 0.0}, config.JevRequestCoverage)); len(gaps) != 0 {
		t.Fatalf("the job's switch is off: %d", len(gaps))
	}
	if gaps, _ := coveredRequest(t, nil); len(gaps) != 0 {
		t.Fatalf("no decision backend, no job: %d", len(gaps))
	}
}

func TestJevCoverageSuspectsAreFiledOnce(t *testing.T) {
	b := &backend{answer: 0.05}
	gaps, w := coveredRequest(t, jobsWith(b))
	if len(gaps) != 2 {
		t.Fatal(len(gaps))
	}
	all, _ := w.Store.Search(kanban.Query{Limit: 50})
	var parent kanban.Item
	for _, it := range all {
		if it.Title == "request: export and import" {
			parent = it
		}
	}
	w.reconcileCoverage(context.Background(), outcome{}, parent)
	w.reconcileCoverage(context.Background(), outcome{}, parent)
	if n := len(gapCards(t, w.Store)); n != 2 {
		t.Fatalf("a repeat files no second card: %d", n)
	}
}

func TestJevCoverageSendsOnlyTitlesAndClauses(t *testing.T) {
	b := &backend{answer: 0.9}
	coveredRequest(t, jobsWith(b))
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.asked) == 0 {
		t.Fatal("the backend was asked")
	}
	for _, r := range b.asked {
		blob := ""
		for _, q := range r.Questions {
			blob += q.Instructions + " "
		}
		if strings.Contains(blob, "belai-") || strings.Contains(blob, "/home/") {
			t.Fatalf("no path or internal id reaches a backend: %q", blob)
		}
	}
}

func TestWorkerGivesTheHandoffToolRelevanceOnlyWithABackend(t *testing.T) {
	got := func(jobs *jev.Jobs) *tools.WorkerClaim {
		var claim *tools.WorkerClaim
		store, reg := testEnv(t)
		store.Add(kanban.ItemInput{Title: "seeded", Labels: []string{"scout", "quality"}}, kanban.Provenance{})
		p := planner()
		w := newWorker(t, store, reg, p, func(ctx context.Context, tt Turn) (run.Result, error) {
			claim = tt.Claim
			return complete(ctx, tt)
		})
		w.Jev = jobs
		w.jevOnce.Do(func() {})
		if err := w.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		return claim
	}
	if c := got(nil); c.Relevance != nil {
		t.Fatal("no backend, no relevance")
	}
	if c := got(jobsWith(&backend{answer: 0.9})); c.Relevance == nil {
		t.Fatal("a backend gives the handoff tool its relevance")
	}
}
