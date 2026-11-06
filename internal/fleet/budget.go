package fleet

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/budget"
	"github.com/vulnetix/belai/internal/run"
	tx "github.com/vulnetix/belai/internal/transcript"
)

// The per-item budgets end a claim; before any of them does, the worker is told
// to finalise (commit, record its findings, publish) once the first of them
// reaches the profile's budget.finalize_at. The cost bound is what the item's
// main-model calls are estimated to cost at the turn's model price: the price
// the Software Factory dispatched, or Belai's built-in table. A model with no
// known price leaves the cost bound off, and the token, pass and wall bounds
// stand in for it.

// itemMeter adds up one item's spend and decides when to finalise and when to
// end the claim. It wraps the shared run-cap meter (internal/budget,
// docs/run-caps.md) in its cancel mode: a fleet worker's claim is cancelled when
// the cost is over the profile's budget, and it never extends. Its methods are
// called from the turn's event goroutine and the wall timer.
type itemMeter struct {
	w          *Worker
	finalizeAt float64
	costBudget int64 // µ$, 0 = off
	tokBudget  int
	maxPasses  int
	cancel     context.CancelCauseFunc
	meter      *budget.Meter
}

func (w *Worker) newMeter(cancel context.CancelCauseFunc) *itemMeter {
	p := w.Profile
	m := &itemMeter{w: w, finalizeAt: p.FinalizeAtOr(), costBudget: p.CostBudgetMicroUSD(), cancel: cancel}
	if b := p.Budget; b != nil {
		m.tokBudget, m.maxPasses = b.MaxTokensPerItem, b.MaxPassesPerItem
	}
	m.meter = budget.NewMeter(budget.Caps{CostMicroUSD: m.costBudget, Tokens: m.tokBudget, Passes: m.maxPasses, FinalizeAt: m.finalizeAt})
	return m
}

// finalise tells the worker to finalise when a verdict says so. A fleet claim
// has no extend step: a reached ceiling ends the claim through the cancel causes
// the worker already has.
func (m *itemMeter) finalise(v budget.Verdict) {
	if v.Level != budget.OK {
		m.w.raiseFinalize(v.Reason)
	}
}

// call adds one main-model call at the turn's price.
func (m *itemMeter) call(u tx.Usage) {
	price := m.w.price.Load()
	if price == nil {
		return
	}
	// The call's tokens are the goal loop's to report (goal), which counts them
	// across rounds; only the cost is added here.
	v := m.meter.Spend(price.MicroUSD(u), 0)
	cost := m.meter.Snapshot().CostMicroUSD
	m.w.liveCost.Store(cost)
	m.finalise(v)
	if m.costBudget > 0 && cost > m.costBudget {
		m.cancel(errCostBudget)
	}
}

// goal reads the goal loop's running totals: the item's tokens across rounds
// and the round's passes.
func (m *itemMeter) goal(tokens, passes int) {
	m.finalise(m.meter.Observe(tokens, passes))
}

// spent is the item's estimated cost so far.
func (m *itemMeter) spent() int64 { return m.meter.Snapshot().CostMicroUSD }

// wallTimer raises the wall-time finalise at finalize_at of the wall budget d.
// The returned function stops it.
func (m *itemMeter) wallTimer(d time.Duration) func() {
	if d <= 0 {
		return func() {}
	}
	t := time.AfterFunc(time.Duration(float64(d)*m.finalizeAt), func() { m.w.raiseFinalize("wall") })
	return func() { t.Stop() }
}

// reached reports whether used is at least frac of limit (budget.Reached).
func reached(used, limit int64, frac float64) bool { return budget.Reached(used, limit, frac) }

// raiseFinalize tells the item in hand to finalise, once: on the running turn's
// goal loop at its next pass boundary, or the next turn's when none is running.
func (w *Worker) raiseFinalize(reason string) {
	f, err := agent.NewFinalizeFact(reason)
	if err != nil {
		return
	}
	w.coordMu.Lock()
	defer w.coordMu.Unlock()
	if w.coord.finalized {
		return
	}
	w.coord.finalized = true
	w.logf("%s: the %s budget is nearly spent; told the agent to finalise", w.Record.Item, reason)
	if w.coord.sink == nil || !w.coord.sink(f) {
		w.coord.finalize = &f
	}
}

// localKinds are provider kinds that run on the machine and cost nothing.
var localKinds = map[string]bool{"ollama": true, "llama-server": true}

// setPrice records the price the turn about to run on cfg is costed at: the
// dispatched price when the factory sent one for this model, otherwise the
// built-in table's. Unknown leaves it nil, which turns the cost bound off.
func (w *Worker) setPrice(cfg run.Config) {
	var p *budget.Price
	switch {
	case w.Limits != nil && w.Limits.Price != nil && w.Limits.Price.Valid() && w.Limits.Price.Known() &&
		(w.Limits.PriceModel == "" || strings.EqualFold(w.Limits.PriceModel, cfg.Model)):
		cp := *w.Limits.Price
		p = &cp
	case localKinds[cfg.Kind]:
		p = &budget.Price{}
	default:
		if bp, ok := budget.PriceFor(cfg.Provider, cfg.Model); ok {
			p = &bp
		}
	}
	if p == nil && w.Profile.CostBudgetMicroUSD() > 0 && w.unpricedLogged.CompareAndSwap(false, true) {
		w.logf("no price is known for %s/%s: budget.max_cost_usd_per_item is off for it; the token, pass and wall bounds apply", cfg.Provider, cfg.Model)
	}
	w.price.Store(p)
}

// priceSlot is the atomic the turn's price lives in.
type priceSlot = atomic.Pointer[budget.Price]
