package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/budget"
	"github.com/vulnetix/belai/internal/modelinfo"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/transcript"
	"github.com/vulnetix/belai/internal/tui/components"
)

// /context and the reply cost line are pure measurement. Everything printed is
// a size the harness already estimated for a request (run.RequestShape), a
// provider-reported token count, a harness tool identifier or a built-in list
// price. No model is asked, no file is read, and the lines are Ephemeral
// system rows: they show in the thread and never reach the session record, a
// model's history, sync or telemetry.

// contextGaugeCells is the width of the text gauge /context prints. It is plain
// characters, not a styled bar, because a system row is wrapped and copied as
// text.
const contextGaugeCells = 30

// replyCost is the estimated cost of one agent call in millionths of a dollar.
// counted is false when the provider reported no token split (the call's
// tokens are an estimate), so nothing is fabricated; known is false when the
// model has no built-in price. The override parameter is the Software
// Factory's price, not a user setting, so it is always nil here.
func replyCost(ev run.UsageEvent) (cost int64, known, counted bool) {
	if ev.Role != run.RoleAgent || ev.Prompt+ev.Completion <= 0 {
		return 0, false, false
	}
	cost, known = budget.CostFor(ev.Provider, ev.Model, nil, transcript.Usage{
		PromptTokens:     ev.Prompt,
		CompletionTokens: ev.Completion,
		TotalTokens:      ev.Prompt + ev.Completion,
		CacheReadTokens:  ev.CacheRead,
		CacheWriteTokens: ev.CacheWrite,
	})
	return cost, known, true
}

// formatMicroUSD renders millionths of a dollar for a human: "$0" for free,
// "<$0.001" under a tenth of a cent, three places under a dollar, two above.
func formatMicroUSD(micro int64) string {
	switch {
	case micro <= 0:
		return "$0"
	case micro < 1000:
		return "<$0.001"
	case micro < 1_000_000:
		return fmt.Sprintf("$%.3f", float64(micro)/1e6)
	}
	return fmt.Sprintf("$%.2f", float64(micro)/1e6)
}

// replyCostLine is the dim line printed after an agent call when
// ui.reply_cost is on: "in 12.3k (cache 90%) · out 410 · ~$0.031 est.". The
// cache share and the cost are left out when the provider reports no cache
// activity and when the model has no known price. It is "" for a call with no
// reported token split.
func replyCostLine(ev run.UsageEvent) string {
	cost, known, counted := replyCost(ev)
	if !counted {
		return ""
	}
	in := "in " + humanTokens(int64(ev.Prompt))
	if ev.CacheRead+ev.CacheWrite > 0 {
		in += fmt.Sprintf(" (cache %d%%)", cachePct(ev.CacheRead, ev.Prompt))
	}
	parts := []string{in, "out " + humanTokens(int64(ev.Completion))}
	if known {
		parts = append(parts, "~"+formatMicroUSD(cost)+" est.")
	}
	return strings.Join(parts, " · ")
}

// addReplyCost prints the reply cost line for one completed call. It does
// nothing unless the user turned ui.reply_cost on and the call was an agent-role
// call (the agent, a subagent or an explorer: one line per such call, not per
// reply the user sees).
func (a *App) addReplyCost(ev run.UsageEvent) {
	if !a.settings.ReplyCostEnabled() || ev.Role != run.RoleAgent {
		return
	}
	if line := replyCostLine(ev); line != "" {
		a.addEphemeralSystem(line)
	}
}

// contextWindow is the context window of the model the last request went to,
// or 0 when none is known. The catalogue window applies only to the selected
// model: another model (a routed or fast-tier call) resolves from the user
// override and the built-in registry alone.
func (a *App) contextWindow(model string) int {
	catalog := 0
	if model == "" || model == a.cfg.Model {
		model, catalog = a.cfg.Model, a.selectedModelWindow()
	}
	if w, ok := modelinfo.ResolveWith(model, a.settings.ContextWindows, catalog); ok && w > 0 {
		return w
	}
	return 0
}

// contextBreakdown measures the last main-turn request against its window.
func (a *App) contextBreakdown() budget.ContextBreakdown {
	r := a.intelReq
	return budget.Breakdown(r.lastShape.System, r.lastShape.ToolDefs, r.lastShape.History,
		r.lastShape.ToolResults, a.contextWindow(r.lastModel))
}

// textGauge draws used/total as a plain-character bar.
func textGauge(percent, cells int) string {
	fill := min(max(percent*cells/100, 0), cells)
	return "[" + strings.Repeat("█", fill) + strings.Repeat("░", cells-fill) + "]"
}

// contextText is the /context output.
func (a *App) contextText() string {
	r := a.intelReq
	if r.calls == 0 || r.lastShape.System+r.lastShape.ToolDefs+r.lastShape.History == 0 {
		return "context: nothing sent yet"
	}
	b := a.contextBreakdown()
	var out []string
	if b.HasWindow {
		out = append(out, fmt.Sprintf("context: %s of %s tokens used (%d%%), estimated",
			humanTokens(int64(b.Used)), humanTokens(int64(b.Window)), b.Percent), "  "+textGauge(b.Percent, contextGaugeCells))
	} else {
		out = append(out, fmt.Sprintf("context: %s tokens sent in the last request, estimated (the model's window is unknown)",
			humanTokens(int64(b.Used))))
	}
	row := func(name string, n int) string {
		return fmt.Sprintf("  %-14s%8s", name, humanTokens(int64(n)))
	}
	out = append(out,
		row("system", b.System),
		row("tool defs", b.ToolDefs),
		row("history", b.History),
		row("tool results", b.ToolResults))
	for _, t := range b.Top {
		name := sanitize.Ident(t.Name, 24)
		if name == "" {
			name = "tool"
		}
		out = append(out, fmt.Sprintf("    %-12s%8s", name, humanTokens(int64(t.Tokens))))
	}
	if b.HasWindow {
		out = append(out, row("free", b.Free))
	}
	out = append(out, a.sessionCostLine())
	return strings.Join(out, "\n")
}

// sessionCostLine is the session's estimated spend over the priced agent calls.
func (a *App) sessionCostLine() string {
	r := a.intelReq
	switch {
	case r.priced == 0 && r.unpriced == 0:
		return "  session cost  not reported yet"
	case r.priced == 0:
		return fmt.Sprintf("  session cost  no price known for this model (%d call(s) not costed)", r.unpriced)
	case r.unpriced > 0:
		return fmt.Sprintf("  session cost  ~%s est. over %d call(s); %d call(s) not costed (no price known)",
			formatMicroUSD(r.costMicro), r.priced, r.unpriced)
	}
	return fmt.Sprintf("  session cost  ~%s est. over %d call(s)", formatMicroUSD(r.costMicro), r.priced)
}

// intelContextLine is the intel screen's window gauge row, fed by the same
// breakdown as /context.
func (a *App) intelContextLine(w int) string {
	if a.intelReq.calls == 0 {
		return components.LowStyle.Render("  context window · nothing sent yet")
	}
	b := a.contextBreakdown()
	if !b.HasWindow {
		return components.LowStyle.Render("  context window · unknown for this model")
	}
	bar := components.LimitBar(float64(b.Percent)/100, -1, 20, components.ColorTeal)
	return components.MutedStyle.Render(fmt.Sprintf("  context window · %s of %s (%d%%) ",
		humanTokens(int64(b.Used)), humanTokens(int64(b.Window)), b.Percent)) + bar
}

// contextCommand prints the /context breakdown as one system row.
func (a *App) contextCommand(string) tea.Cmd {
	a.addEphemeralSystem(a.contextText())
	return nil
}
