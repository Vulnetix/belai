package fleet

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/budget"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sessionsync"
	tx "github.com/vulnetix/belai/internal/transcript"
)

func TestLimitsLayerOverTheProfile(t *testing.T) {
	p := builderProfile()
	p.Budget = &agentprofile.BudgetSpec{MaxPassesPerItem: 8, MaxWallPerItem: "45m"}
	p.MaxIterations = 12
	l := LimitsFrom(&sessionsync.WorkerBudget{MaxCostUSD: 25, MaxAttempts: 4, MaxContinuations: 9, MaxIterations: 40, MaxWall: "4h",
		Price: &sessionsync.WorkerPrice{Model: "m", Input: 3, Output: 15}})
	got := l.Apply(p)
	if got.Budget.MaxCostUSDPerItem != 25 || got.Budget.MaxPassesPerItem != 8 || got.Budget.MaxWallPerItem != "4h0m0s" {
		t.Fatalf("budget = %+v", got.Budget)
	}
	if got.MaxAttemptsOr() != 4 || got.MaxContinuationsOr() != 9 || got.MaxIterations != 40 {
		t.Fatalf("attempts %d continuations %d iterations %d", got.MaxAttemptsOr(), got.MaxContinuationsOr(), got.MaxIterations)
	}
	if p.Budget.MaxCostUSDPerItem != 0 || p.Kanban.MaxAttempts != 2 {
		t.Fatal("Apply changed the profile it was given")
	}
	if l.Price == nil || l.Price.Output != 15 || l.PriceModel != "m" {
		t.Fatalf("price = %+v %q", l.Price, l.PriceModel)
	}
}

func TestADispatchedBudgetIsCleaned(t *testing.T) {
	b := (&sessionsync.WorkerBudget{MaxCostUSD: 1e9, FinalizeAt: 2, MaxAttempts: 99, MaxWall: "-1h",
		Price: &sessionsync.WorkerPrice{Model: "-rf", Input: 3}}).Clean()
	if b.MaxCostUSD != sessionsync.MaxWorkerCostUSD || b.FinalizeAt != 0.95 || b.MaxAttempts != sessionsync.MaxWorkerAttempts || b.MaxWall != "" || b.Price != nil {
		t.Fatalf("cleaned = %+v", b)
	}
	if (&sessionsync.WorkerBudget{MaxPasses: -3}).Clean() != nil {
		t.Fatal("an empty budget survived")
	}
	if got := sessionsync.ParseWorkerBudget((&sessionsync.WorkerBudget{MaxCostUSD: 5}).Encode()); got == nil || got.MaxCostUSD != 5 {
		t.Fatalf("round trip = %+v", got)
	}
}

func TestReachedRoundsUp(t *testing.T) {
	if reached(6, 8, 0.85) || !reached(7, 8, 0.85) || reached(1, 0, 0.85) {
		t.Fatal("reached is off")
	}
}

// costlyTurn reports model calls of 1M input tokens each until the claim is
// cancelled, changing a file first, and records the facts the worker sends.
func costlyTurn(t *testing.T, w **Worker, facts *[]agent.CoordFact, mu *sync.Mutex) TurnRunner {
	return func(ctx context.Context, tt Turn) (run.Result, error) {
		detach := (*w).attachCoord(func(f agent.CoordFact) bool {
			mu.Lock()
			*facts = append(*facts, f)
			mu.Unlock()
			return true
		})
		defer detach()
		if err := os.WriteFile(filepath.Join(tt.Workdir, "fix.txt"), []byte("half\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 100 && ctx.Err() == nil; i++ {
			tt.Emit(agent.Event{Kind: agent.EventModelCallKind, Usage: &tx.Usage{PromptTokens: 1_000_000}})
		}
		return run.Result{StopReason: run.StopCancelled, Passes: 3}, ctx.Err()
	}
}

// The cost bound tells the agent to finalise at finalize_at, once, and ends the
// claim past the bound; the progress made is continued, not failed.
func TestTheCostBoundFinalisesThenEndsTheClaim(t *testing.T) {
	store, reg := testEnv(t)
	it, _, _ := store.Add(kanban.ItemInput{Title: "fix the flake", Labels: []string{"build"}}, kanban.Provenance{})
	p := builderProfile()
	p.Workspace = &agentprofile.WorkspaceSpec{Isolation: agentprofile.IsolationWorktree}
	p.Budget = &agentprofile.BudgetSpec{MaxCostUSDPerItem: 10}
	var facts []agent.CoordFact
	var mu sync.Mutex
	var w *Worker
	w = newWorker(t, store, reg, p, costlyTurn(t, &w, &facts, &mu))
	w.Repo = gitRepo(t)
	w.price.Store(&budget.Price{Input: 1, Output: 1}) // $1 a call
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(it.ID)
	if got.Attempts != 0 || got.Continuations != 1 || !strings.Contains(got.LastNote(), errCostBudget.Error()) {
		t.Fatalf("not continued at the cost bound: %s attempts=%d continuations=%d (%q)", got.List, got.Attempts, got.Continuations, got.LastNote())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(facts) != 1 || facts[0].Event() != agent.CoordFinalize || facts[0].Reason() != "cost" {
		t.Fatalf("facts = %+v", facts)
	}
	if w.Record.CostMicroUSD < 10_000_000 || w.Record.CostMicroUSD > 12_000_000 {
		t.Fatalf("cost recorded = %d µ$", w.Record.CostMicroUSD)
	}
}

// A model with no price leaves the cost bound off.
func TestAnUnpricedModelLeavesTheCostBoundOff(t *testing.T) {
	w := &Worker{Profile: builderProfile()}
	w.setPrice(run.Config{Provider: "custom", Model: "my-finetune"})
	if w.price.Load() != nil {
		t.Fatal("an unknown model got a price")
	}
	w.Limits = &Limits{Price: &budget.Price{Input: 2, Output: 4}, PriceModel: "my-finetune"}
	w.setPrice(run.Config{Provider: "custom", Model: "my-finetune"})
	if p := w.price.Load(); p == nil || p.Output != 4 {
		t.Fatalf("the dispatched price was not used: %+v", p)
	}
	w.setPrice(run.Config{Provider: "custom", Model: "other"})
	if w.price.Load() != nil {
		t.Fatal("a price for one model was used for another")
	}
}
