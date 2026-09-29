package budget

import (
	"strings"
	"time"
)

// Plan-limit windows. A limit's Window names what its Used fraction measures:
// the provider's rolling five-hour and seven-day plan allowances, or the
// per-minute request and token allowances an API key carries.
const (
	WindowFiveHour    = "5h"
	WindowSevenDay    = "7d"
	WindowMinTokens   = "1m-tokens"
	WindowMinInput    = "1m-input"
	WindowMinOutput   = "1m-output"
	WindowMinRequests = "1m-requests"
)

// limitRetention is how long an observed limit is kept: past it the reading is
// too old to say anything about the account.
const limitRetention = 8 * 24 * time.Hour

// PlanLimit is one provider rate or plan limit as the last response header
// reported it. It holds numbers, identifiers and times only. The header text
// itself is never stored, so nothing a provider writes can reach the surfaces
// that render it.
type PlanLimit struct {
	// Provider is the provider id the response came from (a harness constant).
	Provider string `json:"provider"`
	// Window is one of the Window* constants.
	Window string `json:"window"`
	// Used is the share of the allowance spent, 0 to 1.
	Used float64 `json:"used"`
	// Limit and Remaining are the allowance and what is left, in tokens or
	// requests; zero when the provider reported only a utilisation fraction.
	Limit     int64 `json:"limit,omitempty"`
	Remaining int64 `json:"remaining,omitempty"`
	// Reset is when the window ends and the allowance refills.
	Reset time.Time `json:"reset"`
	// ObservedAt is when the response that carried the reading arrived.
	ObservedAt time.Time `json:"observed_at"`
}

// Key is the provider and window the reading is filed under; a newer reading
// for the same key replaces an older one.
func (p PlanLimit) Key() string { return p.Provider + "|" + p.Window }

// Span is the length of the window the limit measures: five hours, seven days
// or one minute. Zero for an unknown window.
func (p PlanLimit) Span() time.Duration {
	switch {
	case p.Window == WindowFiveHour:
		return 5 * time.Hour
	case p.Window == WindowSevenDay:
		return 7 * 24 * time.Hour
	case strings.HasPrefix(p.Window, "1m-"):
		return time.Minute
	}
	return 0
}

// Valid reports whether the reading has a plausible shape at now. It is the
// fail-closed check every reading passes before it is recorded: a provider, a
// known window, a utilisation within 0 to 1, a remaining count that does not
// exceed the limit, and a reset no earlier than an hour ago and no later than
// eight days ahead.
func (p PlanLimit) Valid(now time.Time) bool {
	if p.Provider == "" || p.Span() == 0 {
		return false
	}
	if p.Used < 0 || p.Used > 1 || p.Used != p.Used {
		return false
	}
	if p.Limit < 0 || p.Remaining < 0 || (p.Limit > 0 && p.Remaining > p.Limit) {
		return false
	}
	if p.Reset.Before(now.Add(-time.Hour)) || p.Reset.After(now.Add(limitRetention)) {
		return false
	}
	return true
}

// windowRank orders windows by how much a reader wants to see them: the plan
// windows first, then the per-minute allowances.
func windowRank(w string) int {
	switch w {
	case WindowFiveHour:
		return 0
	case WindowSevenDay:
		return 1
	case WindowMinTokens:
		return 2
	case WindowMinInput:
		return 3
	case WindowMinOutput:
		return 4
	case WindowMinRequests:
		return 5
	}
	return 6
}

// ObserveLimits records the limits one response reported. A reading that fails
// Valid is dropped. Of two readings for the same provider and window the later
// ObservedAt wins, so a stale reload from another process never overwrites a
// fresher observation made here.
func (r *Recorder) ObserveLimits(limits []PlanLimit) {
	if len(limits) == 0 {
		return
	}
	now := r.now()
	r.mu.Lock()
	added := false
	for _, l := range limits {
		if !l.Valid(now) {
			continue
		}
		if l.ObservedAt.IsZero() {
			l.ObservedAt = now
		}
		if cur, ok := r.pending.limits[l.Key()]; ok && cur.ObservedAt.After(l.ObservedAt) {
			continue
		}
		r.pending.limits[l.Key()] = l
		added = true
	}
	r.mu.Unlock()
	if added {
		select {
		case r.kick <- struct{}{}:
		default:
		}
	}
}

// Limits returns the latest reading per window for provider, ordered plan
// windows first. A reading whose window has already reset is left out: its
// utilisation describes a window that no longer exists.
func (r *Recorder) Limits(provider string) []PlanLimit {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.limitsLocked(provider, now)
}

func (r *Recorder) limitsLocked(provider string, now time.Time) []PlanLimit {
	best := map[string]PlanLimit{}
	for _, m := range []map[string]PlanLimit{r.disk.Limits, r.inflight.limits, r.pending.limits} {
		for k, l := range m {
			if l.Provider != provider {
				continue
			}
			if cur, ok := best[k]; !ok || !cur.ObservedAt.After(l.ObservedAt) {
				best[k] = l
			}
		}
	}
	out := make([]PlanLimit, 0, len(best))
	for _, l := range best {
		if l.Reset.Before(now) {
			continue
		}
		out = append(out, l)
	}
	sortLimits(out)
	return out
}

func sortLimits(ls []PlanLimit) {
	for i := 1; i < len(ls); i++ {
		for j := i; j > 0 && windowRank(ls[j].Window) < windowRank(ls[j-1].Window); j-- {
			ls[j], ls[j-1] = ls[j-1], ls[j]
		}
	}
}

// Tightest returns the limit the footer bar and the runway follow: the first
// in window order (five-hour, then seven-day, then the per-minute allowances),
// or nil when there is none.
func Tightest(ls []PlanLimit) *PlanLimit {
	if len(ls) == 0 {
		return nil
	}
	best := ls[0]
	for _, l := range ls[1:] {
		if windowRank(l.Window) < windowRank(best.Window) {
			best = l
		}
	}
	return &best
}
