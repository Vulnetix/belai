package tui

import (
	"fmt"
	"math"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/belai/internal/budget"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tui/components"
)

// Session intelligence pane. f12 opens the runs panel on its intel tab; enter
// in the pane, /intel and the screen switcher open the same numbers full
// screen. Everything drawn is a harness fact read from the usage ledger:
// token counts, hour buckets, identifiers, times and parsed limit readings.

// Intel view modes: the pane's lower half lists the roll-ups (timeline), the
// models of a window, or this session's roles.
const (
	intelModeTimeline = "timeline"
	intelModeModels   = "models"
	intelModeRoles    = "roles"
)

// intelViewState is the pane's and the full screen's state. window indexes
// Intel.Rollups (0 today, 1 week, 2 30 days); sel is the highlighted row of
// the full screen's table (the pane uses the runs panel's own selection).
type intelViewState struct {
	window int
	mode   string
	sel    int
}

// intelRequest sums the estimated size of what this session's agent calls sent,
// by part. Sizes only: it is built from run.UsageEvent.Request, which never
// carries content.
type intelRequest struct {
	calls       int
	system      int
	toolDefs    int
	history     int
	toolResults map[string]int
}

// addRequest folds one agent call's request shape in.
func (r *intelRequest) add(ev run.UsageEvent) {
	if ev.Role != run.RoleAgent {
		return
	}
	r.calls++
	r.system += ev.Request.System
	r.toolDefs += ev.Request.ToolDefs
	r.history += ev.Request.History
	for name, n := range ev.Request.ToolResults {
		if r.toolResults == nil {
			r.toolResults = map[string]int{}
		}
		r.toolResults[name] += n
	}
}

func (r intelRequest) toolTotal() int {
	n := 0
	for _, v := range r.toolResults {
		n += v
	}
	return n
}

var intelWindowNames = [3]string{"today", "this week", "last 30 days"}

// intelSnapshot measures the ledger now for the selected model.
func (a *App) intelSnapshot() (budget.Intel, bool) {
	if a.budgets == nil {
		return budget.Intel{}, false
	}
	return a.budgets.Intel(time.Now(), a.cfg.Provider, a.cycleBudgets()), true
}

// intelMode is the current mode, defaulting to the timeline.
func (a *App) intelMode() string {
	if a.intelState.mode == "" {
		return intelModeTimeline
	}
	return a.intelState.mode
}

func stateStyle(s budget.State) lipgloss.Style {
	switch s {
	case budget.Red:
		return components.DangerStyle
	case budget.Amber:
		return components.WarnStyle
	}
	return components.AccentStyle
}

func stateColour(s budget.State) lipgloss.TerminalColor {
	switch s {
	case budget.Red:
		return components.ColorDanger
	case budget.Amber:
		return components.ColorAmber
	}
	return components.ColorTeal
}

// twoCol lays left and right on one line of width w with right flush right; the
// right side is dropped when both do not fit.
func twoCol(left, right string, w int) string {
	if right == "" {
		return ansi.Truncate(left, w, "…")
	}
	pad := w - lipgloss.Width(left) - lipgloss.Width(right)
	if pad < 2 {
		return ansi.Truncate(left, w, "…")
	}
	return left + strings.Repeat(" ", pad) + right
}

func padTo(s string, n int) string {
	if pad := n - lipgloss.Width(s); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return ansi.Truncate(s, n, "…")
}

// limitTitles are the labels the pane gives each limit window.
var limitTitles = map[string]string{
	budget.WindowFiveHour:    "5-hour limit",
	budget.WindowSevenDay:    "weekly limit",
	budget.WindowMinTokens:   "tokens/min",
	budget.WindowMinInput:    "input tokens/min",
	budget.WindowMinOutput:   "output tokens/min",
	budget.WindowMinRequests: "requests/min",
}

// limitState colours one limit row: red when spent, amber when a plan window
// is being used faster than its clock, teal otherwise.
func limitState(l budget.PlanLimit, now time.Time) budget.State {
	switch {
	case l.Used >= 1:
		return budget.Red
	case l.Span() >= time.Hour && l.Used > limitElapsed(l, now):
		return budget.Amber
	}
	return budget.Teal
}

// intelLimitRow draws one plan limit: title, bar with its reset marker,
// percentage used, and when it resets.
func intelLimitRow(l budget.PlanLimit, now time.Time, w int) string {
	barW := min(30, max(10, w-58))
	st := limitState(l, now)
	title := limitTitles[l.Window]
	bar := components.LimitBar(l.Used, limitElapsed(l, now), barW, stateColour(st))
	pct := stateStyle(st).Render(fmt.Sprintf("%3d%%", int(math.Round(l.Used*100))))
	left := "  " + components.MutedStyle.Render(padTo(title, 18)) + bar + " " + pct
	right := components.MutedStyle.Render("resets in " + budget.FormatDuration(l.Reset.Sub(now)))
	return twoCol(left, right, w)
}

// trendGlyph and trendText describe the trend against the 7-day average.
func trendText(t budget.Trend) (glyph, text string, style lipgloss.Style) {
	switch t.Label {
	case budget.TrendEasing:
		return "↘", fmt.Sprintf("easing · %.1f× the 7-day average", t.Ratio), components.AccentStyle
	case budget.TrendRising:
		return "↗", fmt.Sprintf("rising · %.1f× the 7-day average", t.Ratio), components.WarnStyle
	case budget.TrendSteady:
		return "→", fmt.Sprintf("steady · %.1f× the 7-day average", t.Ratio), components.AccentStyle
	}
	return "·", "no 7-day baseline yet", components.LowStyle
}

func paceStyle(label string) lipgloss.Style {
	switch label {
	case budget.PaceBrisk:
		return components.WarnStyle
	case budget.PaceHot:
		return components.DangerStyle
	case budget.PaceIdle:
		return components.MutedStyle
	}
	return components.AccentStyle
}

// intelAge is how long ago the newest limit reading arrived: "live 8s ago" for
// a fresh one, "as of 3h 5m ago" otherwise; empty with no limits.
func intelAge(in budget.Intel) string {
	var newest time.Time
	for _, l := range in.Limits {
		if l.ObservedAt.After(newest) {
			newest = l.ObservedAt
		}
	}
	if newest.IsZero() {
		return ""
	}
	age := in.Now.Sub(newest)
	if age < 0 {
		age = 0
	}
	if age < time.Minute {
		return fmt.Sprintf("live %ds ago", int(age.Seconds()))
	}
	return "as of " + budget.FormatDuration(age) + " ago"
}

// intelSummaryLine is the pane's first line: the selected model on the left,
// today's totals and the age of the limit reading on the right.
func (a *App) intelSummaryLine(in budget.Intel, w int) string {
	who := a.cfg.Provider
	if who == "" {
		who = "no provider"
	}
	left := "  " + components.MutedStyle.Render(who)
	if a.cfg.Model != "" {
		left += components.MutedStyle.Render(" · ") + components.EmphStyle.Render(a.cfg.Model)
	}
	td := in.Rollups[0]
	right := components.MutedStyle.Render("today ") + components.EmphStyle.Render(humanTokens(td.Tokens))
	if td.Calls > 0 {
		right += components.MutedStyle.Render(fmt.Sprintf(" · %d calls", td.Calls))
	}
	right += components.MutedStyle.Render(fmt.Sprintf(" · %s", nounCount(td.Sessions, "session", "sessions")))
	if age := intelAge(in); age != "" {
		right += components.LowStyle.Render("    " + age)
	}
	return twoCol(left, right, w)
}

// intelPaceLines are the pace and trend line and the runway line.
func intelPaceLines(in budget.Intel, w int) []string {
	pace := "  " + components.MutedStyle.Render("pace     ") + paceStyle(in.Pace.Label).Render(in.Pace.Label)
	if in.Pace.PctPerHour > 0 {
		pace += components.MutedStyle.Render(fmt.Sprintf(" · %.1f%%/h", in.Pace.PctPerHour))
	}
	pace += components.MutedStyle.Render(" · " + humanTokens(int64(in.Pace.TokensPerHour)) + " tok/h")
	glyph, text, style := trendText(in.Trend)
	trend := components.MutedStyle.Render("trend  ") + style.Render(glyph+" "+text)

	rwStyle := stateStyle(in.State)
	rw := "  " + components.MutedStyle.Render("runway   ")
	switch in.Runway.Kind {
	case budget.RunwayNone:
		rw += components.LowStyle.Render(in.Runway.Text)
	default:
		rw += rwStyle.Render(in.Runway.Text)
		rw += components.LowStyle.Render(" · " + in.Runway.Kind)
	}
	return []string{twoCol(pace, trend, w), ansi.Truncate(rw, w, "…")}
}

// intelHeaderLines are the pane's fixed lines: in the timeline mode the summary,
// the limits (or why there are none), pace, trend and runway; in the models and
// roles modes a one-line sub-header for the list below.
func (a *App) intelHeaderLines(w int) []string {
	in, ok := a.intelSnapshot()
	if !ok {
		return []string{components.MutedStyle.Render("  token usage is unavailable (the ledger did not open)")}
	}
	if mode := a.intelMode(); mode != intelModeTimeline {
		return []string{a.intelSubHeader(in, mode, w)}
	}
	lines := []string{a.intelSummaryLine(in, w)}
	if len(in.Limits) == 0 {
		who := in.Provider
		if who == "" {
			who = "this provider"
		}
		lines = append(lines, components.LowStyle.Render("  no plan limit reported by "+who+" · b set a budget"))
	}
	for _, l := range in.Limits {
		lines = append(lines, intelLimitRow(l, in.Now, w))
	}
	return append(lines, intelPaceLines(in, w)...)
}

// intelSubHeader labels the models and roles lists.
func (a *App) intelSubHeader(in budget.Intel, mode string, w int) string {
	ru := in.Rollups[a.intelState.window]
	name := intelWindowNames[a.intelState.window]
	var left, right string
	if mode == intelModeRoles {
		var total int64
		for _, r := range in.Roles {
			total += r.Tokens
		}
		left = "  " + components.MutedStyle.Render("roles · this session · ") + components.EmphStyle.Render(humanTokens(total))
		right = components.LowStyle.Render("t timeline · m models")
	} else {
		left = "  " + components.MutedStyle.Render("models · ") + components.EmphStyle.Render(name) +
			components.MutedStyle.Render(" · "+humanTokens(ru.Tokens)+" · "+nounCount(ru.Sessions, "session", "sessions"))
		right = components.LowStyle.Render("←→ window · t timeline · r roles")
	}
	return twoCol(left, right, w)
}

// intelShare is v as a share of total, 0 to 1.
func intelShare(v, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(v) / float64(total)
}

// intelItems are the pane's list rows for the current mode.
func (a *App) intelItems() []runsItem {
	in, ok := a.intelSnapshot()
	if !ok {
		return nil
	}
	return a.intelItemsFor(in, a.intelMode())
}

// intelItemsFor builds the list rows of one mode from a snapshot.
func (a *App) intelItemsFor(in budget.Intel, mode string) []runsItem {
	teal := components.ColorTeal
	switch mode {
	case intelModeModels:
		ru := in.Rollups[a.intelState.window]
		items := make([]runsItem, 0, len(ru.ByModel))
		for _, m := range ru.ByModel {
			share := intelShare(m.Tokens, ru.Tokens)
			items = append(items, runsItem{
				ID:     "intel:" + m.Key,
				Label:  m.Key,
				Row:    components.MutedStyle.Render(padTo(m.Key, 34)) + components.LimitBar(share, -1, 20, teal),
				Detail: components.MutedStyle.Render(fmt.Sprintf("%7s  %3d%%", humanTokens(m.Tokens), int(math.Round(share*100)))),
			})
		}
		return items
	case intelModeRoles:
		var total int64
		for _, r := range in.Roles {
			total += r.Tokens
		}
		items := make([]runsItem, 0, len(in.Roles))
		for _, r := range in.Roles {
			share := intelShare(r.Tokens, total)
			items = append(items, runsItem{
				ID:     "intel:" + r.Role,
				Label:  r.Role,
				Row:    components.MutedStyle.Render(padTo(r.Role, 14)) + components.LimitBar(share, -1, 20, teal),
				Detail: components.MutedStyle.Render(fmt.Sprintf("%7s  %3d%%", humanTokens(r.Tokens), int(math.Round(share*100)))),
			})
		}
		return items
	}
	// Timeline: today as the 24-hour sparkline, then the week and the 30 days
	// as shares of the 30-day total.
	spark := components.Sparkline(in.Spark[:], 24, lipgloss.NewStyle().Foreground(teal), components.LowStyle)
	total := in.Rollups[2].Tokens
	rows := make([]runsItem, 0, 3)
	for i, ru := range in.Rollups {
		var pic string
		switch i {
		case 0:
			pic = spark + components.LowStyle.Render(" 24h")
		default:
			pic = components.LimitBar(intelShare(ru.Tokens, total), -1, 24, teal)
		}
		rows = append(rows, runsItem{
			ID:     fmt.Sprintf("intel:%d", i),
			Label:  intelWindowNames[i],
			Row:    components.MutedStyle.Render(padTo(intelWindowNames[i], 14)) + pic,
			Detail: components.MutedStyle.Render(fmt.Sprintf("%7s  %s", humanTokens(ru.Tokens), nounCount(ru.Sessions, "session", "sessions"))),
		})
	}
	return rows
}

// intelPanelLayout returns the pane's header lines and how many list rows fit.
// The pane takes half the terminal's height instead of the third the other tabs
// take; when the terminal is short the header drops lines from its bottom,
// keeping at least one list row.
func (a *App) intelPanelLayout(w int) (header []string, maxRows int) {
	header = a.intelHeaderLines(w)
	total := a.height/2 - 3
	if total < 2 {
		total = 2
	}
	if len(header) > total-1 {
		header = header[:total-1]
	}
	maxRows = max(1, total-len(header))
	return header, maxRows
}

// handleRunsIntelKey handles the intel tab's own keys once the runs panel has
// taken the shared ones (esc, f9, f12, tab, up and down).
func (a *App) handleRunsIntelKey(m tea.KeyMsg) tea.Cmd {
	switch m.String() {
	case "left", "h":
		a.intelState.window = (a.intelState.window + 2) % 3
	case "right", "l":
		a.intelState.window = (a.intelState.window + 1) % 3
	case "m":
		a.intelState.mode = intelModeModels
		a.runsSel, a.runsScroll = 0, 0
	case "r":
		a.intelState.mode = intelModeRoles
		a.runsSel, a.runsScroll = 0, 0
	case "t":
		a.intelState.mode = intelModeTimeline
		a.runsSel, a.runsScroll = 0, 0
	case "b":
		return a.openBudgets()
	case "enter":
		return a.push(viewIntel)
	}
	return nil
}

// openIntel pushes the full-screen intel view.
func (a *App) openIntel() tea.Cmd { return a.push(viewIntel) }

func (a *App) enterIntel() tea.Cmd {
	a.intelState.sel = 0
	if a.intelState.mode == intelModeTimeline {
		a.intelState.mode = intelModeModels
	}
	if a.budgets != nil {
		a.budgets.Refresh(0)
	}
	return nil
}

// handleIntelKey handles the full screen's keys.
func (a *App) handleIntelKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	rows := 0
	if in, ok := a.intelSnapshot(); ok {
		if a.intelState.mode == intelModeRoles {
			rows = len(in.Roles)
		} else {
			rows = len(in.Rollups[a.intelState.window].ByModel)
		}
	}
	switch m.String() {
	case "esc", "q":
		a.pop()
	case "up", "k":
		if a.intelState.sel > 0 {
			a.intelState.sel--
		}
	case "down", "j":
		if a.intelState.sel < rows-1 {
			a.intelState.sel++
		}
	case "left", "h":
		a.intelState.window = (a.intelState.window + 2) % 3
		a.intelState.sel = 0
	case "right", "l":
		a.intelState.window = (a.intelState.window + 1) % 3
		a.intelState.sel = 0
	case "m", "t":
		a.intelState.mode = intelModeModels
		a.intelState.sel = 0
	case "r":
		a.intelState.mode = intelModeRoles
		a.intelState.sel = 0
	case "R":
		if a.budgets != nil {
			a.budgets.Refresh(0)
		}
	case "b":
		return a, a.openBudgets()
	}
	return a, nil
}

// heatShades are the heatmap's four steps, faintest first.
var heatShades = [4]struct {
	r     string
	style lipgloss.Style
}{
	{"░", components.LineStyle},
	{"▒", components.LowStyle},
	{"▓", components.AccentStyle},
	{"█", lipgloss.NewStyle().Foreground(components.ColorTealSoft)},
}

// heatRow draws one day's 24 hours, each quantised against peak, the week's
// busiest hour. hours after nowHour (>= 0) are drawn as dots: they have not
// happened yet.
func heatRow(row [24]int64, peak int64, nowHour int) string {
	var b strings.Builder
	for h, v := range row {
		if nowHour >= 0 && h > nowHour {
			b.WriteString(components.LowStyle.Render("·"))
			continue
		}
		lvl := 0
		switch {
		case v <= 0 || peak <= 0:
			lvl = 0
		case float64(v)/float64(peak) <= 0.25:
			lvl = 1
		case float64(v)/float64(peak) <= 0.6:
			lvl = 2
		default:
			lvl = 3
		}
		b.WriteString(heatShades[lvl].style.Render(heatShades[lvl].r))
	}
	return b.String()
}

// intelHeat draws the seven-day, hour-by-hour heatmap. A day with tokens but no
// hour buckets (imported history) is one flat row of the second shade: it has
// no hourly shape to draw and none is invented.
func intelHeat(in budget.Intel, w int) []string {
	var peak int64
	for _, row := range in.Heat {
		for _, v := range row {
			if v > peak {
				peak = v
			}
		}
	}
	axis := components.LowStyle.Render("  7 days · tokens per hour     ") +
		components.LowStyle.Render("00  04  08  12  16  20")
	lines := []string{ansi.Truncate(axis, w, "")}
	for i := 0; i < 7; i++ {
		day := "   "
		if t, err := time.Parse("2006-01-02", in.HeatDays[i]); err == nil {
			day = strings.ToLower(t.Weekday().String()[:3])
		}
		nowHour := -1
		if i == 6 {
			nowHour = in.Now.Hour()
		}
		var cells string
		switch {
		case in.HeatDayOnly[i]:
			cells = components.LowStyle.Render(strings.Repeat("▒", 24))
		default:
			cells = heatRow(in.Heat[i], peak, nowHour)
		}
		left := "  " + components.MutedStyle.Render(day+"  ") + cells
		right := components.MutedStyle.Render(humanTokens(in.HeatDayTokens[i]))
		if in.HeatDayOnly[i] {
			right += components.LowStyle.Render(" day total")
		}
		lines = append(lines, twoCol(left, right, w))
	}
	return lines
}

// intelCompositionLine draws what this session's agent calls sent, by part.
func (a *App) intelCompositionLine(w int) string {
	r := a.intelReq
	if r.calls == 0 {
		return components.LowStyle.Render("  this session · nothing sent yet")
	}
	parts := []struct {
		name string
		n    int
	}{{"system", r.system}, {"tool defs", r.toolDefs}, {"history", r.history}, {"tool results", r.toolTotal()}}
	var peak int
	for _, p := range parts {
		peak = max(peak, p.n)
	}
	var segs []string
	for _, p := range parts {
		bar := components.LimitBar(intelShare(int64(p.n), int64(max(peak, 1))), -1, 6, components.ColorTeal)
		segs = append(segs, components.MutedStyle.Render(p.name+" "+humanTokens(int64(p.n))+" ")+bar)
	}
	return ansi.Truncate("  "+strings.Join(segs, "  "), w, "…")
}

// intelView renders the full screen.
func (a *App) intelView() string {
	w := a.contentWidth()
	var b strings.Builder
	b.WriteString(components.SectionHeader("Session intelligence", "esc back", w))
	in, ok := a.intelSnapshot()
	if !ok {
		b.WriteString(components.MutedStyle.Render("Token usage is unavailable: the usage ledger did not open.") + "\n")
		return lipgloss.NewStyle().Padding(1).Render(b.String())
	}
	inner := max(w-2, 20)
	b.WriteString(a.intelSummaryLine(in, inner) + "\n")
	if len(in.Limits) == 0 {
		who := in.Provider
		if who == "" {
			who = "this provider"
		}
		b.WriteString(components.LowStyle.Render("  no plan limit reported by "+who) + "\n")
	}
	for _, l := range in.Limits {
		b.WriteString(intelLimitRow(l, in.Now, inner) + "\n")
	}
	for _, bd := range a.cycleBudgets() {
		if bd.Scope == "session" {
			continue
		}
		g := budget.Status(bd, a.budgets.Used(bd), in.Now)
		fg := toFooterGauge(g)
		start, end := budget.Window(bd.Scope, in.Now)
		elapsed := 1 - float64(end.Sub(in.Now))/float64(end.Sub(start))
		left := "  " + components.MutedStyle.Render(padTo(bd.Scope+" budget", 18)) +
			components.LimitBar(fg.UsedFrac, elapsed, 30, fg.Colour()) +
			" " + lipgloss.NewStyle().Foreground(fg.Colour()).Render(fmt.Sprintf("%3d%% left", g.TokenPctLeft))
		b.WriteString(twoCol(left, components.MutedStyle.Render(fg.TimeLeft+" left"), inner) + "\n")
	}
	for _, l := range intelPaceLines(in, inner) {
		b.WriteString(l + "\n")
	}
	b.WriteString("\n")
	for _, l := range intelHeat(in, inner) {
		b.WriteString(l + "\n")
	}
	b.WriteString("\n")

	mode := a.intelMode()
	if mode == intelModeTimeline {
		mode = intelModeModels
	}
	b.WriteString(a.intelSubHeader(in, mode, inner) + "\n")
	items := a.intelItemsFor(in, mode)
	for i, it := range items {
		// The pre-styled row keeps its own colours; the marker carries selection.
		b.WriteString(twoCol(components.Cursor(i == a.intelState.sel)+it.Row, it.Detail, inner) + "\n")
	}
	if len(items) == 0 {
		b.WriteString(components.LowStyle.Render("  nothing recorded yet") + "\n")
	}
	b.WriteString("\n" + components.LowStyle.Render("  sent this session, estimated") + "\n")
	b.WriteString(a.intelCompositionLine(inner) + "\n")
	b.WriteString("\n" + components.HelpBar("↑↓", "select", "←→", "window", "m", "models", "r", "roles", "b", "budgets", "R", "re-read ledger", "esc", "back") + "\n")
	return lipgloss.NewStyle().Padding(1).Render(b.String())
}

// intelStatus summarises the ledger for the screen switcher's row.
func (a *App) intelStatus() string {
	in, ok := a.intelSnapshot()
	if !ok {
		return ""
	}
	return fmt.Sprintf("today %s · %s", humanTokens(in.Rollups[0].Tokens), in.Pace.Label)
}
