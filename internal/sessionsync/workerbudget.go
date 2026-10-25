package sessionsync

import (
	"encoding/json"
	"math"
	"regexp"
	"time"
)

// WorkerBudget is the Software Factory's budget and retry settings for the
// workers a "worker" or "crew" request starts: the tenant's agent settings with
// the workflow's and the step's overrides resolved by the server. A zero field
// keeps the profile's own value. Price, when set, is what the dispatched Model
// costs, so the per-item cost bound is reckoned at the price the factory bills
// by. Everything in it is untrusted: Clean bounds every number and drops what
// it cannot read.
type WorkerBudget struct {
	MaxCostUSD       float64      `json:"maxCostUsd,omitempty"`
	FinalizeAt       float64      `json:"finalizeAt,omitempty"`
	MaxContinuations int          `json:"maxContinuations,omitempty"`
	MaxAttempts      int          `json:"maxAttempts,omitempty"`
	MaxWall          string       `json:"maxWall,omitempty"`
	MaxPasses        int          `json:"maxPasses,omitempty"`
	MaxTokens        int          `json:"maxTokens,omitempty"`
	MaxIterations    int          `json:"maxIterations,omitempty"`
	Price            *WorkerPrice `json:"price,omitempty"`
}

// WorkerPrice is one model's price in US dollars per million tokens.
type WorkerPrice struct {
	Model      string  `json:"model,omitempty"`
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead,omitempty"`
	CacheWrite float64 `json:"cacheWrite,omitempty"`
}

// Bounds a dispatched worker budget is clamped to.
const (
	MaxWorkerCostUSD       = 10000.0
	MaxWorkerContinuations = 50
	MaxWorkerAttempts      = 10
	MaxWorkerWall          = 7 * 24 * time.Hour
	MaxWorkerPasses        = 1000
	MaxWorkerTokens        = 1 << 40
	MaxWorkerIterations    = 400
	MaxWorkerPrice         = 1000.0
	minWorkerFinalizeAt    = 0.5
	maxWorkerFinalizeAt    = 0.95
)

// EnvWorkerBudget carries a dispatch's cleaned worker budget from belai rc to
// the workers it starts, and from a crew run's supervisor to its members, the
// way the trigger travels.
const EnvWorkerBudget = "BELAI_WORKER_BUDGET"

var priceModel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,127}$`)

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func clampInt(v, hi int) int {
	if v <= 0 {
		return 0
	}
	return min(v, hi)
}

// Clean returns a copy with every field in range, or nil when nothing usable is
// left. An out-of-range number is clamped, an unreadable one dropped.
func (b *WorkerBudget) Clean() *WorkerBudget {
	if b == nil {
		return nil
	}
	c := WorkerBudget{
		MaxContinuations: clampInt(b.MaxContinuations, MaxWorkerContinuations),
		MaxAttempts:      clampInt(b.MaxAttempts, MaxWorkerAttempts),
		MaxPasses:        clampInt(b.MaxPasses, MaxWorkerPasses),
		MaxTokens:        clampInt(b.MaxTokens, MaxWorkerTokens),
		MaxIterations:    clampInt(b.MaxIterations, MaxWorkerIterations),
	}
	if finite(b.MaxCostUSD) && b.MaxCostUSD > 0 {
		c.MaxCostUSD = math.Min(b.MaxCostUSD, MaxWorkerCostUSD)
	}
	if finite(b.FinalizeAt) && b.FinalizeAt > 0 {
		c.FinalizeAt = math.Max(minWorkerFinalizeAt, math.Min(b.FinalizeAt, maxWorkerFinalizeAt))
	}
	if d, err := time.ParseDuration(b.MaxWall); err == nil && d > 0 {
		c.MaxWall = min(d, MaxWorkerWall).String()
	}
	if p := b.Price; p != nil && (p.Model == "" || priceModel.MatchString(p.Model)) {
		ok := true
		for _, v := range []float64{p.Input, p.Output, p.CacheRead, p.CacheWrite} {
			if !finite(v) || v < 0 || v > MaxWorkerPrice {
				ok = false
			}
		}
		if ok && (p.Input > 0 || p.Output > 0) {
			cp := *p
			c.Price = &cp
		}
	}
	if c == (WorkerBudget{}) {
		return nil
	}
	return &c
}

// Encode is the cleaned budget as JSON for EnvWorkerBudget, or "" for none.
func (b *WorkerBudget) Encode() string {
	c := b.Clean()
	if c == nil {
		return ""
	}
	data, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	return string(data)
}

// ParseWorkerBudget reads an EnvWorkerBudget value and cleans it again: the
// environment is only as trusted as the dispatch that filled it.
func ParseWorkerBudget(raw string) *WorkerBudget {
	if raw == "" || len(raw) > 4096 {
		return nil
	}
	var b WorkerBudget
	if json.Unmarshal([]byte(raw), &b) != nil {
		return nil
	}
	return b.Clean()
}
