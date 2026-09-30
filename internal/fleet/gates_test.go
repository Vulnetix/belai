package fleet

import (
	"context"
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/testdetect"
)

func TestWorkerTellsTheHandoffToolWhatSuitesAreDetected(t *testing.T) {
	store, reg := testEnv(t)
	store.Add(kanban.ItemInput{Title: "scout it", Labels: []string{"scout"}}, kanban.Provenance{})
	p := builderProfile()
	p.Name = "t-scout"
	p.Kanban.Labels = []string{"scout"}
	p.Kanban.HandoffLabels = []string{"build"}
	p.Kanban.Gates = &agentprofile.GatesSpec{Require: true}
	var claim = make(chan *Turn, 1)
	w := newWorker(t, store, reg, p, func(ctx context.Context, tt Turn) (run.Result, error) {
		claim <- &tt
		return complete(ctx, tt)
	})
	w.Suites = func(context.Context) []testdetect.Suite {
		return []testdetect.Suite{{Name: "go", Ecosystem: "go"}, {Name: "just-check", Ecosystem: "just"}}
	}
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	tt := <-claim
	c := tt.Claim
	if !c.GatesRequired || c.GateRoot != w.Repo || len(c.GateSuites) != 2 || c.GateSuites[0].Name != "go" || c.GateSuites[0].Ecosystem != "go" {
		t.Fatalf("gate facts on the claim: %+v", c)
	}
}

func TestWorkerWithoutAGatesBlockKeepsTheHandoffAsItWas(t *testing.T) {
	store, reg := testEnv(t)
	store.Add(kanban.ItemInput{Title: "do it", Labels: []string{"build"}}, kanban.Provenance{})
	var got Turn
	w := newWorker(t, store, reg, builderProfile(), func(ctx context.Context, tt Turn) (run.Result, error) {
		got = tt
		return complete(ctx, tt)
	})
	w.Suites = func(context.Context) []testdetect.Suite {
		t.Error("a worker without gates must not scan the repository for suites")
		return nil
	}
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got.Claim.GatesRequired || got.Claim.GateSuites != nil || got.Claim.GateRoot != "" {
		t.Fatalf("no gate facts without a gates block: %+v", got.Claim)
	}
}

func TestWorkerRoutesAQualityCardsHandoffsByClarityWhenAuto(t *testing.T) {
	store, reg := testEnv(t)
	store.Add(kanban.ItemInput{Title: "seeded", Labels: []string{"scout", agentprofile.QualityLabel}}, kanban.Provenance{})
	p := builderProfile()
	p.Name, p.Kanban.Labels, p.Kanban.HandoffLabels = "t-scout", []string{"scout"}, []string{"build"}
	p.Kanban.Quality = &agentprofile.QualitySpec{Sweep: true, List: agentprofile.ListAuto}
	var got Turn
	w := newWorker(t, store, reg, p, func(ctx context.Context, tt Turn) (run.Result, error) {
		got = tt
		return complete(ctx, tt)
	})
	w.Suites = func(context.Context) []testdetect.Suite { return nil }
	w.Once = true
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got.Claim == nil || !got.Claim.HandoffAuto || got.Claim.HandoffList != kanban.Review {
		t.Fatalf("a seeded card's handoffs are routed by clarity, falling back to review: %+v", got.Claim)
	}
}

func TestQualityCardWithAFixedListIsNotRoutedByClarity(t *testing.T) {
	store, reg := testEnv(t)
	store.Add(kanban.ItemInput{Title: "seeded", Labels: []string{"scout", agentprofile.QualityLabel}}, kanban.Provenance{})
	p := builderProfile()
	p.Name, p.Kanban.Labels, p.Kanban.HandoffLabels = "t-scout", []string{"scout"}, []string{"build"}
	p.Kanban.Quality = &agentprofile.QualitySpec{Sweep: true, List: "backlog"}
	var got Turn
	w := newWorker(t, store, reg, p, func(ctx context.Context, tt Turn) (run.Result, error) {
		got = tt
		return complete(ctx, tt)
	})
	w.Suites = func(context.Context) []testdetect.Suite { return nil }
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got.Claim.HandoffAuto || got.Claim.HandoffList != kanban.Backlog {
		t.Fatalf("a fixed list stays fixed: %+v", got.Claim)
	}
}
