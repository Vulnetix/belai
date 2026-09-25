package tui

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/budget"
	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/tui/components"
)

// usageMsg carries one model call's usage out of the run package's observer
// into the Bubble Tea loop, where the footer and budget warnings react to it.
type usageMsg run.UsageEvent

// budgetRefreshAge is how stale the in-memory ledger may get before the tick
// re-reads usage.json, so day and month spend recorded by another signet
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
	a.usageCancel = run.SetUsageObserver(func(ev run.UsageEvent) {
		rec.Add(ev.Provider, ev.Model, int64(ev.Tokens))
		select {
		case a.usageEvents <- ev:
		default:
		}
	})
}

// importHistory folds in the usage of sessions the ledger never saw (saved
// before token budgets existed, or by an older signet), so day and month
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

// budgetGauge is the footer gauge for now: nil when the routing is "routed"
// (no single model serves the turn) or the selected model has no budget;
// otherwise the budget the cycle is on.
func (a *App) budgetGauge(now time.Time) *components.BudgetGauge {
	if a.budgets == nil || routedModelCount(a.cfg) > 0 {
		return nil
	}
	bs := a.settings.BudgetsFor(a.cfg.Provider, a.cfg.Model)
	if len(bs) == 0 {
		return nil
	}
	b := bs[budget.CycleIndex(len(bs), a.settings.BudgetCycle(), now)]
	return toFooterGauge(budget.Status(b, a.budgets.Used(b), now))
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
func formatTimeLeft(d time.Duration) string {
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
