package fleet

import (
	"os"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/budget"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// Limits are the Software Factory's budget and retry settings for a worker,
// layered over its profile: the organisation's agent settings with the
// workflow's and the step's overrides, resolved by the server and dispatched
// with the start request. A zero field keeps the profile's value.
type Limits struct {
	MaxCostUSDPerItem float64
	FinalizeAt        float64
	MaxContinuations  int
	MaxAttempts       int
	MaxWallPerItem    string
	MaxPassesPerItem  int
	MaxTokensPerItem  int
	MaxIterations     int
	// Price is what PriceModel costs (any model when PriceModel is empty).
	Price      *budget.Price
	PriceModel string
}

// LimitsFrom converts a dispatched worker budget, cleaned; nil for none.
func LimitsFrom(b *sessionsync.WorkerBudget) *Limits {
	c := b.Clean()
	if c == nil {
		return nil
	}
	l := &Limits{
		MaxCostUSDPerItem: c.MaxCostUSD, FinalizeAt: c.FinalizeAt,
		MaxContinuations: c.MaxContinuations, MaxAttempts: c.MaxAttempts,
		MaxWallPerItem: c.MaxWall, MaxPassesPerItem: c.MaxPasses, MaxTokensPerItem: c.MaxTokens,
		MaxIterations: c.MaxIterations,
	}
	if p := c.Price; p != nil {
		l.Price = &budget.Price{Input: p.Input, Output: p.Output, CacheRead: p.CacheRead, CacheWrite: p.CacheWrite}
		l.PriceModel = p.Model
	}
	return l
}

// LimitsFromEnv reads the limits belai rc handed down in the environment
// (sessionsync.EnvWorkerBudget), or nil.
func LimitsFromEnv() *Limits {
	return LimitsFrom(sessionsync.ParseWorkerBudget(os.Getenv(sessionsync.EnvWorkerBudget)))
}

// Apply returns p with the limits layered over its budget, attempts,
// continuations and iterations. p's own blocks are copied, never changed.
func (l *Limits) Apply(p agentprofile.AgentProfile) agentprofile.AgentProfile {
	if l == nil {
		return p
	}
	b := agentprofile.BudgetSpec{}
	if p.Budget != nil {
		b = *p.Budget
	}
	if l.MaxCostUSDPerItem > 0 {
		b.MaxCostUSDPerItem = l.MaxCostUSDPerItem
	}
	if l.FinalizeAt > 0 {
		b.FinalizeAt = l.FinalizeAt
	}
	if l.MaxWallPerItem != "" {
		b.MaxWallPerItem = l.MaxWallPerItem
	}
	if l.MaxPassesPerItem > 0 {
		b.MaxPassesPerItem = l.MaxPassesPerItem
	}
	if l.MaxTokensPerItem > 0 {
		b.MaxTokensPerItem = l.MaxTokensPerItem
	}
	if b != (agentprofile.BudgetSpec{}) {
		p.Budget = &b
	}
	if p.Kanban != nil && (l.MaxAttempts > 0 || l.MaxContinuations > 0) {
		k := *p.Kanban
		if l.MaxAttempts > 0 {
			k.MaxAttempts = l.MaxAttempts
		}
		if l.MaxContinuations > 0 {
			k.MaxContinuations = l.MaxContinuations
		}
		p.Kanban = &k
	}
	if l.MaxIterations > 0 {
		p.MaxIterations = l.MaxIterations
	}
	return p
}
