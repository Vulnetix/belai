package budget

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/vulnetix/belai/internal/config"
)

// Session intelligence: roll-ups, pace, trend and runway computed from the
// ledger. Everything here reads harness facts (token counts, hour buckets,
// identifiers, times and parsed limit readings) and nothing a model or a
// provider wrote as text.
//
// Sourcing rule: pace, the 24-hour sparkline, the heatmap and call counts read
// hour buckets only; roll-ups and trend read day totals. Imported transcripts
// know only days, so they count in the windows and the trend but never invent
// an hourly shape.

// Pace and trend labels.
const (
	PaceIdle        = "idle"
	PaceActive      = "active"
	PaceComfortable = "comfortable"
	PaceBrisk       = "brisk"
	PaceHot         = "hot"

	TrendNew    = "new"
	TrendEasing = "easing"
	TrendSteady = "steady"
	TrendRising = "rising"
)

// Runway kinds: what the projection was measured against.
const (
	RunwayLimit  = "limit"  // a plan window from a response header
	RunwayBudget = "budget" // a day or month token budget
	RunwayNone   = "none"   // nothing to measure against
)

// Trend thresholds against the 7-day average at the same point of the day.
const (
	trendEasingBelow = 0.8
	trendRisingAbove = 1.25
)

// ModelTokens is one model's tokens in a window.
type ModelTokens struct {
	Key    string
	Tokens int64
}

// RoleTokens is one usage role's tokens in this process.
type RoleTokens struct {
	Role   string
	Tokens int64
}

// Rollup is one window's totals: today, the last 7 days or the last 30 days
// (each including today).
type Rollup struct {
	Label    string
	Start    time.Time
	End      time.Time
	Tokens   int64
	Sessions int
	// Calls counts completed model calls in the window from hour buckets, which
	// are kept for eight days: the 30-day window's count covers those eight.
	Calls   int
	ByModel []ModelTokens
}

// Pace is how fast tokens are being spent now.
type Pace struct {
	// TokensPerHour is the trailing 60 minutes from hour buckets.
	TokensPerHour float64
	// PctPerHour is the share of the tightest plan window spent per hour, 0
	// when there is no window to calibrate against.
	PctPerHour float64
	Label      string
}

// Trend compares today with the previous seven days at the same point of the
// day.
type Trend struct {
	Ratio float64
	Label string
}

// Runway says whether the allowance lasts to the end of its window.
type Runway struct {
	Kind string
	// Lasts reports that the allowance outlasts the window at this pace.
	Lasts bool
	// Until is when the allowance runs out at this pace; Reset is when its
	// window ends. Both are zero for RunwayNone.
	Until time.Duration
	Reset time.Duration
	Text  string
}

// Intel is everything the footer slot, the pane and the full screen draw.
type Intel struct {
	Now      time.Time
	Provider string
	// Limits are the provider's latest plan-limit readings, plan windows first.
	Limits  []PlanLimit
	Pace    Pace
	Trend   Trend
	Runway  Runway
	Rollups [3]Rollup
	// Spark is tokens per hour for the last 24 hours, oldest first, ending with
	// the current hour.
	Spark [24]int64
	// Heat is tokens per hour for the last seven local days, oldest first.
	// HeatDays names each row's date; HeatDayTokens is that day's total from
	// day totals; HeatDayOnly marks a day that has tokens but no hour buckets
	// (imported history), which has no hourly shape to draw.
	Heat          [7][24]int64
	HeatDays      [7]string
	HeatDayTokens [7]int64
	HeatDayOnly   [7]bool
	// Roles is this process's tokens by usage role, largest first.
	Roles []RoleTokens
	State State
}

// Intel measures the ledger at now for provider and the budgets of the
// selected model. Roll-ups, pace, trend, the sparkline and the heatmap cover
// every model; only the limits (the provider's) and the runway (the limits and
// budgets of the selected model) are specific.
func (r *Recorder) Intel(now time.Time, provider string, budgets []config.TokenBudget) Intel {
	r.mu.Lock()
	defer r.mu.Unlock()

	loc := now.Location()
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, loc)

	in := Intel{Now: now, Provider: provider}
	byModel := r.mergedDaysLocked()
	hours := r.mergedHoursLocked()

	// Roll-ups.
	spans := [3]struct {
		label string
		days  int
	}{{"today", 1}, {"week", 7}, {"30 days", 30}}
	for i, sp := range spans {
		start := today.AddDate(0, 0, -(sp.days - 1))
		in.Rollups[i] = r.rollupLocked(sp.label, start, now, byModel, hours)
	}

	// Sparkline and heatmap from hour buckets.
	curHour := time.Date(y, m, d, now.Hour(), 0, 0, 0, loc)
	for i := 0; i < 24; i++ {
		h := curHour.Add(-time.Duration(23-i) * time.Hour)
		in.Spark[i] = hours[h.Format(hourLayout)].T
	}
	dayTotals := map[string]int64{}
	for _, days := range byModel {
		for day, n := range days {
			dayTotals[day] += n
		}
	}
	for row := 0; row < 7; row++ {
		day := today.AddDate(0, 0, -(6 - row))
		ds := day.Format(dayLayout)
		in.HeatDays[row] = ds
		in.HeatDayTokens[row] = dayTotals[ds]
		var sum int64
		for h := 0; h < 24; h++ {
			n := hours[fmt.Sprintf("%sT%02d", ds, h)].T
			in.Heat[row][h] = n
			sum += n
		}
		in.HeatDayOnly[row] = in.HeatDayTokens[row] > 0 && sum == 0
	}

	// Pace: the trailing 60 minutes, the elapsed part of this hour plus the
	// matching share of the previous one.
	frac := float64(now.Minute()*60+now.Second()) / 3600
	prevHour := curHour.Add(-time.Hour)
	cur := float64(hours[curHour.Format(hourLayout)].T)
	prev := float64(hours[prevHour.Format(hourLayout)].T)
	in.Pace.TokensPerHour = cur + prev*(1-frac)

	in.Limits = r.limitsLocked(provider, now)
	in.Trend = trendLocked(now, today, dayTotals)
	in.Runway, in.Pace.PctPerHour = r.runwayLocked(now, in.Limits, budgets, in.Pace.TokensPerHour, hours)
	in.Pace.Label = paceLabel(in.Pace.TokensPerHour, in.Runway)
	in.State = intelState(in.Limits, in.Runway)

	for role, n := range r.roles {
		in.Roles = append(in.Roles, RoleTokens{Role: role, Tokens: n})
	}
	sort.Slice(in.Roles, func(i, j int) bool {
		if in.Roles[i].Tokens != in.Roles[j].Tokens {
			return in.Roles[i].Tokens > in.Roles[j].Tokens
		}
		return in.Roles[i].Role < in.Roles[j].Role
	})
	return in
}

// Intel windows never look past now, so a rollup's End is now.
func (r *Recorder) rollupLocked(label string, start, now time.Time, byModel map[string]map[string]int64, hours map[string]hourBucket) Rollup {
	ru := Rollup{Label: label, Start: start, End: now}
	loc := now.Location()
	inWindow := map[string]bool{}
	for d := start; !d.After(now); d = d.AddDate(0, 0, 1) {
		inWindow[d.Format(dayLayout)] = true
	}
	for key, days := range byModel {
		var n int64
		for day, v := range days {
			if inWindow[day] {
				n += v
			}
		}
		if n > 0 {
			ru.Tokens += n
			ru.ByModel = append(ru.ByModel, ModelTokens{Key: key, Tokens: n})
		}
	}
	sort.Slice(ru.ByModel, func(i, j int) bool {
		if ru.ByModel[i].Tokens != ru.ByModel[j].Tokens {
			return ru.ByModel[i].Tokens > ru.ByModel[j].Tokens
		}
		return ru.ByModel[i].Key < ru.ByModel[j].Key
	})

	startHour := start.Format(dayLayout) + "T00"
	for h, b := range hours {
		if h >= startHour {
			ru.Calls += b.C
		}
	}

	sessions := map[string]bool{}
	for id, e := range r.disk.Sessions {
		if e != nil && inWindow[e.Updated.In(loc).Format(dayLayout)] {
			sessions[id] = true
		}
	}
	for id, day := range r.disk.Imported {
		if inWindow[day] {
			sessions[id] = true
		}
	}
	// This process's own session counts once it has recorded anything, even
	// before its first flush reaches disk.
	if r.session != "" && (len(r.pending.ses[r.session]) > 0 || len(r.inflight.ses[r.session]) > 0) {
		sessions[r.session] = true
	}
	ru.Sessions = len(sessions)
	return ru
}

// trendLocked compares today's tokens with the mean of the previous seven
// days, scaled to the share of today that has elapsed.
func trendLocked(now, today time.Time, dayTotals map[string]int64) Trend {
	var prevSum int64
	for i := 1; i <= 7; i++ {
		prevSum += dayTotals[today.AddDate(0, 0, -i).Format(dayLayout)]
	}
	if prevSum == 0 {
		return Trend{Label: TrendNew}
	}
	dayLen := today.AddDate(0, 0, 1).Sub(today)
	elapsed := float64(now.Sub(today)) / float64(dayLen)
	// Early in the day the expected share is tiny and the ratio wild; floor it
	// at one hour of the day.
	if elapsed < 1.0/24 {
		elapsed = 1.0 / 24
	}
	expected := float64(prevSum) / 7 * elapsed
	ratio := float64(dayTotals[today.Format(dayLayout)]) / expected
	label := TrendSteady
	switch {
	case ratio < trendEasingBelow:
		label = TrendEasing
	case ratio > trendRisingAbove:
		label = TrendRising
	}
	return Trend{Ratio: ratio, Label: label}
}

// runwayLocked projects the tightest allowance forward at the current pace. A
// plan window (five-hour, seven-day) is calibrated from this ledger: the share
// of the window spent per token recorded since the window opened. Without a
// calibrated plan window a day or month budget is used, and without either
// there is nothing to project. It also returns the calibrated percent of the
// window spent per hour for the pace line.
func (r *Recorder) runwayLocked(now time.Time, limits []PlanLimit, budgets []config.TokenBudget, pace float64, hours map[string]hourBucket) (Runway, float64) {
	type proj struct {
		ratio float64
		until time.Duration
		reset time.Duration
		pct   float64
		out   bool
		kind  string
	}
	var best *proj
	consider := func(p proj) {
		if best == nil || p.ratio < best.ratio {
			c := p
			best = &c
		}
	}

	for _, l := range limits {
		if l.Span() < time.Hour {
			continue
		}
		toReset := l.Reset.Sub(now)
		if toReset <= 0 {
			continue
		}
		if l.Used >= 1 {
			consider(proj{ratio: 0, until: 0, reset: toReset, out: true, kind: RunwayLimit})
			continue
		}
		if pace <= 0 {
			consider(proj{ratio: math.Inf(1), reset: toReset, kind: RunwayLimit})
			continue
		}
		wt := tokensSince(hours, l.Reset.Add(-l.Span()))
		if wt <= 0 || l.Used <= 0 {
			continue
		}
		rate := pace * l.Used / float64(wt) // share of the window per hour
		if rate <= 0 {
			continue
		}
		hoursToFull := (1 - l.Used) / rate
		until := time.Duration(hoursToFull * float64(time.Hour))
		consider(proj{ratio: hoursToFull / toReset.Hours(), until: until, reset: toReset, pct: rate * 100, kind: RunwayLimit})
	}

	if best == nil {
		for _, b := range budgets {
			if b.Scope != config.BudgetScopeDay && b.Scope != config.BudgetScopeMonth {
				continue
			}
			used := r.usedLocked(b)
			g := Status(b, used, now)
			if g.TimeLeft <= 0 {
				continue
			}
			if used >= b.Tokens {
				consider(proj{ratio: 0, reset: g.TimeLeft, out: true, kind: RunwayBudget})
				continue
			}
			if pace <= 0 {
				consider(proj{ratio: math.Inf(1), reset: g.TimeLeft, kind: RunwayBudget})
				continue
			}
			hoursToOut := float64(b.Tokens-used) / pace
			consider(proj{
				ratio: hoursToOut / g.TimeLeft.Hours(),
				until: time.Duration(hoursToOut * float64(time.Hour)),
				reset: g.TimeLeft,
				kind:  RunwayBudget,
			})
		}
	}

	if best == nil {
		return Runway{Kind: RunwayNone, Text: "no limit set"}, 0
	}
	rw := Runway{Kind: best.kind, Reset: best.reset, Until: best.until}
	switch {
	case best.out:
		rw.Text = "exhausted, refills in " + FormatDuration(best.reset)
	case best.ratio >= 1:
		rw.Lasts = true
		rw.Text = "lasts past the reset"
	default:
		rw.Text = fmt.Sprintf("runs out in %s, %s before the reset", FormatDuration(best.until), FormatDuration(best.reset-best.until))
	}
	return rw, best.pct
}

// tokensSince totals hour buckets from the hour containing start onward.
func tokensSince(hours map[string]hourBucket, start time.Time) int64 {
	from := start.Format(hourLayout)
	var n int64
	for h, b := range hours {
		if h >= from {
			n += b.T
		}
	}
	return n
}

// paceLabel names the pace: idle when nothing was spent in the last hour, and
// otherwise how the runway compares with the window. Without a runway a
// non-zero pace is just "active".
func paceLabel(pace float64, rw Runway) string {
	if pace <= 0 {
		return PaceIdle
	}
	if rw.Kind == RunwayNone {
		return PaceActive
	}
	if rw.Lasts {
		return PaceComfortable
	}
	if rw.Reset > 0 && float64(rw.Until)/float64(rw.Reset) >= 0.25 {
		return PaceBrisk
	}
	return PaceHot
}

// intelState is the footer colour: red when a plan window or budget is
// exhausted, amber when the pace outruns the clock, teal otherwise.
func intelState(limits []PlanLimit, rw Runway) State {
	for _, l := range limits {
		if l.Span() >= time.Hour && l.Used >= 1 {
			return Red
		}
	}
	if rw.Kind != RunwayNone && !rw.Lasts {
		if rw.Until == 0 {
			return Red
		}
		return Amber
	}
	return Teal
}

// mergedDaysLocked returns day totals by model across the disk snapshot, the
// in-flight write and pending usage. The result is a fresh copy.
func (r *Recorder) mergedDaysLocked() map[string]map[string]int64 {
	out := map[string]map[string]int64{}
	for _, src := range []map[string]map[string]int64{r.disk.Days, r.inflight.days, r.pending.days} {
		for key, days := range src {
			if out[key] == nil {
				out[key] = map[string]int64{}
			}
			for day, n := range days {
				out[key][day] += n
			}
		}
	}
	return out
}

// mergedHoursLocked returns hour buckets summed across models and across the
// disk snapshot, the in-flight write and pending usage.
func (r *Recorder) mergedHoursLocked() map[string]hourBucket {
	out := map[string]hourBucket{}
	for _, src := range []map[string]map[string]hourBucket{r.disk.Hours, r.inflight.hours, r.pending.hours} {
		for _, byHour := range src {
			for hour, b := range byHour {
				cur := out[hour]
				cur.T += b.T
				cur.C += b.C
				out[hour] = cur
			}
		}
	}
	return out
}

// FormatDuration renders a duration at two units of precision: "3d 4h",
// "5h 12m", "42m", or "<1m" in the final minute.
func FormatDuration(d time.Duration) string {
	if d < time.Minute {
		return "<1m"
	}
	days := int(d / (24 * time.Hour))
	hours := int(d/time.Hour) % 24
	mins := int(d/time.Minute) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}
