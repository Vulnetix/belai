package tui

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/budget"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tui/components"
)

// usageMsg carries one model call's usage out of the run package's observer
// into the Bubble Tea loop, where the footer and budget warnings react to it.
type usageMsg run.UsageEvent

// budgetRefreshAge is how stale the in-memory ledger may get before the tick
// re-reads usage.json, so day and month spend recorded by another belai
// process reaches this footer.
const budgetRefreshAge = 30 * time.Second

// initBudgets opens the usage ledger and registers the process-wide usage
// observer. The observer records every call itself — Recorder.Add is safe from
// any goroutine and never waits on disk — so usage is never dropped; only the
// render notification rides a buffered channel and may drop on overflow, which
// is harmless because the next tick redraws the footer anyway.
func (a *App) initBudgets() {
	a.usageEvents = make(chan run.UsageEvent, 256)
	path, err := budget.DefaultPath()
	if err == nil {
		retention := 0
		if a.settings.SessionRetentionDays != nil {
			retention = *a.settings.SessionRetentionDays
		}
		a.budgets, err = budget.Open(path, a.sessionID, retention)
	}
	if err != nil {
		a.addSystem("token budgets unavailable: " + err.Error())
		return
	}
	if n := a.budgets.Notice(); n != "" {
		a.addSystem(n)
	}
	rec := a.budgets
	run.SetPlanLimits(a.settings.PlanLimitsEnabled())
	a.usageCancel = run.SetUsageObserver(func(ev run.UsageEvent) {
		rec.AddCall(ev.Provider, ev.Model, ev.Role, int64(ev.Tokens))
		rec.ObserveLimits(ev.Limits)
		select {
		case a.usageEvents <- ev:
		default:
		}
	})
}

// importHistory folds in the usage of sessions the ledger never saw (saved
// before token budgets existed, or by an older belai), so day and month
// budgets count every session. It is an Init command, so Bubble Tea runs it in
// the background once per start: startup never waits on parsing transcripts,
// and the footer picks the totals up on the next tick.
func (a *App) importHistory() tea.Cmd {
	rec := a.budgets
	if rec == nil {
		return nil
	}
	return func() tea.Msg {
		if dir, err := config.SessionsDir(); err == nil {
			_, _ = rec.ImportHistory(dir)
		}
		return nil
	}
}

// nextUsage reads one usage notification off the channel and re-arms.
func (a *App) nextUsage() tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-a.usageEvents
		if !ok {
			return nil
		}
		return usageMsg(ev)
	}
}

// closeBudgets detaches the observer and flushes the ledger. Idempotent.
func (a *App) closeBudgets() {
	if a.usageCancel != nil {
		a.usageCancel()
		a.usageCancel = nil
	}
	if a.budgets != nil {
		_ = a.budgets.Close()
	}
}

// handleUsage reacts to one completed model call: redraw the footer, and when
// budget warnings are on and the call was to the selected provider+model,
// print one line for each of that model's budgets that is amber or red.
func (a *App) handleUsage(ev run.UsageEvent) {
	a.intelReq.add(ev)
	a.refreshFooter()
	if a.budgets == nil || !a.settings.BudgetWarnEnabled() {
		return
	}
	if ev.Provider != a.cfg.Provider || ev.Model != a.cfg.Model {
		return
	}
	for _, g := range a.budgets.Gauges(a.settings.BudgetsFor(ev.Provider, ev.Model)) {
		if line := budgetWarning(g); line != "" {
			a.addSystem(line)
		}
	}
}

// budgetWarning is the system line for one gauge, or "" when it is teal.
func budgetWarning(g budget.Gauge) string {
	who := config.ModelKey(g.Budget.Provider, g.Budget.Model)
	switch g.State {
	case budget.Red:
		return fmt.Sprintf("budget: %s exhausted — %s of %s tokens used (%s)",
			g.Budget.Scope, humanTokens(g.Used), humanTokens(g.Budget.Tokens), who)
	case budget.Amber:
		return fmt.Sprintf("budget: %s — %d%% of tokens left with %d%% of the %s left (%s)",
			g.Budget.Scope, g.TokenPctLeft, g.TimePctLeft, g.Budget.Scope, who)
	}
	return ""
}

// cycleBudgets is the budgets the footer cycle walks: the selected model's, in
// scope order, and none when the routing is "routed" (no single model serves
// the turn).
func (a *App) cycleBudgets() []config.TokenBudget {
	if routedModelCount(a.cfg) > 0 {
		return nil
	}
	return a.settings.BudgetsFor(a.cfg.Provider, a.cfg.Model)
}

// cycleSlot says what the footer's right side shows at now: the index of the
// budget the cycle is on, or intel when the cycle is on the session
// intelligence slot. The cycle is the model's budgets followed by one intel
// slot (when ui.intel is on), so with no budget, or under "routed", intel is
// the only slot and usage is always visible.
func (a *App) cycleSlot(now time.Time) (idx int, intel, ok bool) {
	if a.budgets == nil {
		return 0, false, false
	}
	n := len(a.cycleBudgets())
	slots := n
	if a.settings.IntelEnabled() {
		slots++
	}
	if slots == 0 {
		return 0, false, false
	}
	i := budget.CycleIndex(slots, a.settings.BudgetCycle(), now)
	return i, i >= n, true
}

// budgetGauge is the footer budget gauge for now: nil when the cycle is on the
// intel slot, the routing is "routed", or the selected model has no budget;
// otherwise the budget the cycle is on.
func (a *App) budgetGauge(now time.Time) *components.BudgetGauge {
	idx, intel, ok := a.cycleSlot(now)
	if !ok || intel {
		return nil
	}
	b := a.cycleBudgets()[idx]
	return toFooterGauge(budget.Status(b, a.budgets.Used(b), now))
}

// intelHint reports whether now is in the last third of the cycle period: the
// stretch of the intel slot that carries the shortcut hint. It follows the
// clock like the cycle itself, so every belai window agrees.
func (a *App) intelHint(now time.Time) bool {
	period := a.settings.BudgetCycle()
	if period <= 0 {
		return false
	}
	phase := time.Duration(now.UnixNano() % int64(period))
	return phase*3 >= period*2
}

// intelGauge is the session intelligence gauge for now: nil unless the cycle is
// on the intel slot.
func (a *App) intelGauge(now time.Time) *components.IntelGauge {
	_, intel, ok := a.cycleSlot(now)
	if !ok || !intel {
		return nil
	}
	in := a.budgets.Intel(now, a.cfg.Provider, a.cycleBudgets())
	return toIntelGauge(in, a.intelHint(now))
}

// limitLabels are the short names the footer gives each limit window.
var limitLabels = map[string]string{
	budget.WindowFiveHour:    "5h",
	budget.WindowSevenDay:    "7d",
	budget.WindowMinTokens:   "tok/min",
	budget.WindowMinInput:    "in/min",
	budget.WindowMinOutput:   "out/min",
	budget.WindowMinRequests: "req/min",
}

// limitElapsed is the share of a limit's window that has passed at now, 0 to 1.
func limitElapsed(l budget.PlanLimit, now time.Time) float64 {
	span := l.Span()
	if span <= 0 {
		return 0
	}
	e := 1 - float64(l.Reset.Sub(now))/float64(span)
	return math.Min(1, math.Max(0, e))
}

// toIntelGauge converts the ledger's intel into the footer's gauge.
func toIntelGauge(in budget.Intel, hint bool) *components.IntelGauge {
	g := &components.IntelGauge{
		Today:       humanTokens(in.Rollups[0].Tokens),
		LimitFrac:   -1,
		ElapsedFrac: -1,
		Spark:       in.Spark[:],
		Pace:        in.Pace.Label,
		State:       components.BudgetState(in.State),
		Hint:        hint,
	}
	if l := budget.Tightest(in.Limits); l != nil {
		g.Limit = fmt.Sprintf("%s %d%%", limitLabels[l.Window], int(math.Round(l.Used*100)))
		g.LimitFrac = l.Used
		g.ElapsedFrac = limitElapsed(*l, in.Now)
	}
	return g
}

func toFooterGauge(g budget.Gauge) *components.BudgetGauge {
	out := &components.BudgetGauge{
		Scope:    g.Budget.Scope,
		TokenPct: g.TokenPctLeft,
		State:    components.BudgetState(g.State),
	}
	if g.Budget.Tokens > 0 {
		out.UsedFrac = float64(g.Used) / float64(g.Budget.Tokens)
	}
	if g.HasTime() {
		out.TimeLeft = formatTimeLeft(g.TimeLeft)
	}
	return out
}

// formatTimeLeft renders a window's remaining time at two units of
// precision: "3d 4h", "5h 12m", "42m", or "<1m" in the final minute.
func formatTimeLeft(d time.Duration) string { return budget.FormatDuration(d) }

// humanTokens renders a token count compactly: 950, 12.5k, 1.2M, 3B.
func humanTokens(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return trimDecimal(float64(n)/1e9) + "B"
	case n >= 1_000_000:
		return trimDecimal(float64(n)/1e6) + "M"
	case n >= 1_000:
		return trimDecimal(float64(n)/1e3) + "k"
	}
	return strconv.FormatInt(n, 10)
}

func trimDecimal(v float64) string {
	s := strconv.FormatFloat(math.Floor(v*10)/10, 'f', 1, 64)
	return strings.TrimSuffix(s, ".0")
}

// parseBudgetTokens reads a budget allowance: a positive whole number, optionally
// with a k, M or B suffix (case-insensitive) and a decimal part before it —
// "250k", "1.5M", "2b" — with commas, underscores and spaces ignored.
func parseBudgetTokens(s string) (int64, error) {
	v := strings.NewReplacer(",", "", "_", "", " ", "").Replace(strings.TrimSpace(s))
	if v == "" {
		return 0, fmt.Errorf("enter a token count, e.g. 250k or 1.5M")
	}
	mult := 1.0
	switch strings.ToLower(v[len(v)-1:]) {
	case "k":
		mult, v = 1e3, v[:len(v)-1]
	case "m":
		mult, v = 1e6, v[:len(v)-1]
	case "b":
		mult, v = 1e9, v[:len(v)-1]
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("%q is not a token count (e.g. 250k or 1.5M)", s)
	}
	n := int64(math.Round(f * mult))
	if n <= 0 {
		return 0, fmt.Errorf("a budget must be at least 1 token")
	}
	return n, nil
}
