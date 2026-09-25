// Package budget tracks token spend per provider+model and measures it against
// the user's token budgets (config.TokenBudget): the scope windows, the
// remaining-token and remaining-time percentages, and the teal/amber/red state
// the footer and the budgets screen colour by. The usage ledger that persists
// day and month totals across sessions and processes lives in ledger.go.
//
// docs/token-budgets.md is the normative description; its numbered rules and
// edge cases are pinned by tests named after them.
package budget

import (
	"math"
	"time"

	"github.com/vulnetix/signet/internal/config"
)

// State is a budget's colour state.
type State int

const (
	// Teal: tokens are lasting at least as long as the time window.
	Teal State = iota
	// Amber: a larger share of the time window remains than of the tokens, so
	// at the current rate the budget runs out before the window ends. Never
	// set for a session budget, which has no time window.
	Amber
	// Red: the budget is exhausted (used >= limit).
	Red
)

func (s State) String() string {
	switch s {
	case Amber:
		return "amber"
	case Red:
		return "red"
	default:
		return "teal"
	}
}

// Gauge is one budget measured at one instant.
type Gauge struct {
	Budget config.TokenBudget
	Used   int64
	// TokenPctLeft is the share of the allowance left, 0–100, rounded up so
	// 0 appears only when the budget is exhausted.
	TokenPctLeft int
	// TimePctLeft is the share of the scope's window left, 0–100, rounded
	// down; -1 for a session budget, which has no window.
	TimePctLeft int
	// TimeLeft is the time until the window ends; 0 for a session budget.
	TimeLeft time.Duration
	State    State
}

// HasTime reports whether the budget's scope has a time window.
func (g Gauge) HasTime() bool { return g.TimePctLeft >= 0 }

// Window returns the scope's window containing now, in now's location: a day
// runs from local midnight to the next local midnight (23 or 25 hours across a
// daylight-saving change), a month from the 1st to the 1st. A session has no
// window and returns two zero times.
func Window(scope string, now time.Time) (start, end time.Time) {
	y, m, d := now.Date()
	loc := now.Location()
	switch scope {
	case config.BudgetScopeDay:
		start = time.Date(y, m, d, 0, 0, 0, 0, loc)
		return start, start.AddDate(0, 0, 1)
	case config.BudgetScopeMonth:
		start = time.Date(y, m, 1, 0, 0, 0, 0, loc)
		return start, start.AddDate(0, 1, 0)
	}
	return time.Time{}, time.Time{}
}

// Status measures budget b with used tokens spent at now. Red when used has
// reached the allowance; otherwise amber when the scope has a window and a
// larger fraction of the window than of the tokens remains; otherwise teal.
// The comparison uses exact fractions, so display rounding never flips it.
func Status(b config.TokenBudget, used int64, now time.Time) Gauge {
	g := Gauge{Budget: b, Used: used, TimePctLeft: -1}
	tokenFrac := 0.0
	if b.Tokens > 0 && used < b.Tokens {
		tokenFrac = float64(b.Tokens-max(used, 0)) / float64(b.Tokens)
	}
	g.TokenPctLeft = int(math.Ceil(tokenFrac * 100))

	start, end := Window(b.Scope, now)
	timeFrac := -1.0
	if !start.IsZero() {
		total := end.Sub(start)
		g.TimeLeft = end.Sub(now)
		timeFrac = float64(g.TimeLeft) / float64(total)
		g.TimePctLeft = int(math.Floor(timeFrac * 100))
	}

	switch {
	case b.Tokens <= 0 || used >= b.Tokens:
		g.State = Red
	case timeFrac >= 0 && timeFrac > tokenFrac:
		g.State = Amber
	default:
		g.State = Teal
	}
	return g
}

// Worst returns the most severe state among gauges (red > amber > teal).
func Worst(gs []Gauge) State {
	w := Teal
	for _, g := range gs {
		if g.State > w {
			w = g.State
		}
	}
	return w
}

// CycleIndex picks which of n budgets the footer shows at now when it cycles
// every period: each budget is shown for one period in turn. It needs no timer
// of its own; the footer recomputes it on every render.
func CycleIndex(n int, period time.Duration, now time.Time) int {
	if n <= 1 || period <= 0 {
		return 0
	}
	return int((now.UnixNano() / int64(period)) % int64(n))
}
