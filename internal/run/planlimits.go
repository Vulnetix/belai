package run

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/vulnetix/belai/internal/budget"
)

// Plan limits. A provider reports how much of an allowance a call left in its
// response headers. parsePlanLimits is the only reader of those headers for
// this purpose and it returns numbers, identifiers and times: the header text
// is never stored, logged or rendered, and any value that is not a number of
// the expected shape is dropped, along with anything implausible (see
// budget.PlanLimit.Valid).

// planLimitsOff turns header parsing off; the zero value leaves it on.
var planLimitsOff atomic.Bool

// SetPlanLimits turns the parsing of provider rate-limit headers on or off for
// the process. It is on by default.
func SetPlanLimits(on bool) { planLimitsOff.Store(!on) }

// parsePlanLimits reads the plan and rate limits a response's headers report.
// Three families are understood:
//
//   - Anthropic's unified plan windows, anthropic-ratelimit-unified-5h-* and
//     -7d-*, a utilisation fraction and a reset in epoch seconds or RFC 3339;
//   - Anthropic's per-key allowances, anthropic-ratelimit-{requests,tokens,
//     input-tokens,output-tokens}-{limit,remaining,reset}, reset in RFC 3339;
//   - the OpenAI dialect's x-ratelimit-{limit,remaining,reset}-{requests,
//     tokens}, reset as a duration ("6m0s", "120ms"), and OpenRouter's bare
//     x-ratelimit-{limit,remaining,reset}, reset in epoch milliseconds.
//
// Providers that send none of these (Copilot, Kiro, Workers AI, the AI
// Gateway) yield nothing.
func parsePlanLimits(provider string, h http.Header, now time.Time) []budget.PlanLimit {
	if planLimitsOff.Load() || provider == "" || len(h) == 0 {
		return nil
	}
	var out []budget.PlanLimit
	add := func(l budget.PlanLimit) {
		l.Provider = provider
		l.ObservedAt = now
		if l.Valid(now) {
			out = append(out, l)
		}
	}

	// Anthropic unified plan windows.
	for _, w := range []string{budget.WindowFiveHour, budget.WindowSevenDay} {
		used, ok := parseFraction(h.Get("anthropic-ratelimit-unified-" + w + "-utilization"))
		if !ok {
			continue
		}
		reset, ok := parseResetTime(h.Get("anthropic-ratelimit-unified-"+w+"-reset"), now)
		if !ok {
			continue
		}
		add(budget.PlanLimit{Window: w, Used: used, Reset: reset})
	}

	// Anthropic per-key allowances.
	for _, f := range []struct{ name, window string }{
		{"requests", budget.WindowMinRequests},
		{"tokens", budget.WindowMinTokens},
		{"input-tokens", budget.WindowMinInput},
		{"output-tokens", budget.WindowMinOutput},
	} {
		limit, ok1 := parseCount(h.Get("anthropic-ratelimit-" + f.name + "-limit"))
		rem, ok2 := parseCount(h.Get("anthropic-ratelimit-" + f.name + "-remaining"))
		reset, ok3 := parseResetTime(h.Get("anthropic-ratelimit-"+f.name+"-reset"), now)
		if ok1 && ok2 && ok3 && limit > 0 {
			add(budget.PlanLimit{Window: f.window, Used: usedShare(limit, rem), Limit: limit, Remaining: rem, Reset: reset})
		}
	}

	// OpenAI-dialect allowances.
	for _, f := range []struct{ name, window string }{
		{"requests", budget.WindowMinRequests},
		{"tokens", budget.WindowMinTokens},
	} {
		limit, ok1 := parseCount(h.Get("x-ratelimit-limit-" + f.name))
		rem, ok2 := parseCount(h.Get("x-ratelimit-remaining-" + f.name))
		reset, ok3 := parseResetDuration(h.Get("x-ratelimit-reset-"+f.name), now)
		if ok1 && ok2 && ok3 && limit > 0 {
			add(budget.PlanLimit{Window: f.window, Used: usedShare(limit, rem), Limit: limit, Remaining: rem, Reset: reset})
		}
	}

	// OpenRouter's bare headers: a request allowance, reset in epoch ms.
	if provider == "openrouter" {
		limit, ok1 := parseCount(h.Get("x-ratelimit-limit"))
		rem, ok2 := parseCount(h.Get("x-ratelimit-remaining"))
		reset, ok3 := parseResetTime(h.Get("x-ratelimit-reset"), now)
		if ok1 && ok2 && ok3 && limit > 0 {
			add(budget.PlanLimit{Window: budget.WindowMinRequests, Used: usedShare(limit, rem), Limit: limit, Remaining: rem, Reset: reset})
		}
	}
	return out
}

// usedShare is the spent share of an allowance, clamped to 0 to 1; Valid still
// rejects a remaining count above the limit.
func usedShare(limit, remaining int64) float64 {
	if limit <= 0 {
		return 0
	}
	// Spent over limit, not one minus a ratio, so 1 of 50 is exactly 0.02.
	u := float64(limit-remaining) / float64(limit)
	return math.Min(1, math.Max(0, u))
}

// parseFraction reads a utilisation fraction, 0 to 1.
func parseFraction(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f > 1 {
		return 0, false
	}
	return f, true
}

// parseCount reads a non-negative whole number.
func parseCount(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// parseResetTime reads a reset instant: an all-digit epoch (seconds, or
// milliseconds from 1e12 up) or an RFC 3339 time.
func parseResetTime(s string, now time.Time) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n <= 0 {
			return time.Time{}, false
		}
		if n >= 1_000_000_000_000 {
			return time.UnixMilli(n), true
		}
		return time.Unix(n, 0), true
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// parseResetDuration reads a reset that arrives as a time to wait: a Go-style
// duration ("6m0s", "120ms") or bare seconds ("20", "1.5").
func parseResetDuration(s string, now time.Time) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if d, err := time.ParseDuration(s); err == nil && d >= 0 {
		return now.Add(d), true
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil && f >= 0 && !math.IsInf(f, 0) && !math.IsNaN(f) {
		return now.Add(time.Duration(f * float64(time.Second))), true
	}
	return time.Time{}, false
}
