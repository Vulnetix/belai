package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/belai/internal/modelinfo"
	"github.com/vulnetix/belai/internal/planest"
	"github.com/vulnetix/belai/internal/tui/components"
	"github.com/vulnetix/belai/internal/webask"
)

// The plan review offers a model to execute the plan on, and estimates what
// each approve route re-sends. Approval writes the plan into the system
// prompt (prompt.CarrierPlan), so the first execute request misses the cache
// whichever model runs it: switching model here costs no cache that approving
// would not already lose. The choice is session-only, like the /model
// picker's "session" scope and the ctrl+q swap: no settings file is written.

// planWarnPct is the share of a model's context window above which a route's
// estimate is shown amber.
const planWarnPct = 80

// planModelOffer lists the models the review offers: the session's model,
// its fast tier, then every model of each configured provider, at most
// webask.MaxPlanModels.
func (a *App) planModelOffer() []webask.PlanModel {
	var out []webask.PlanModel
	idx := map[string]int{}
	add := func(provider, model, label string, window int) int {
		if provider == "" || model == "" {
			return -1
		}
		key := provider + "/" + model
		if i, ok := idx[key]; ok {
			return i
		}
		if len(out) >= webask.MaxPlanModels {
			return -1
		}
		if w, ok := modelinfo.ResolveWith(model, a.settings.ContextWindows, window); ok {
			window = w
		}
		if label == model {
			label = ""
		}
		idx[key] = len(out)
		out = append(out, webask.PlanModel{Provider: provider, Model: model, Label: label, ContextWindow: window})
		return len(out) - 1
	}
	cur := a.currentTarget()
	if i := add(cur.provider, cur.model, "", a.selectedModelWindow()); i >= 0 {
		out[i].Current = true
	}
	if f := a.cfg.Routing.Fast; f != nil {
		if i := add(f.Provider, f.Model, "", 0); i >= 0 {
			out[i].Fast = true
		}
	}
	providers := []string{cur.provider}
	if a.resolver != nil {
		providers = append(providers, a.resolver.ConfiguredProviders()...)
	}
	for _, p := range providers {
		for _, m := range a.catalogFor(p) {
			add(p, m.ID, m.Label, m.ContextWindow)
		}
	}
	return out
}

// planTarget is the model the plan will execute on: the reviewer's pick, or
// the session's model.
func (a *App) planTarget() modelTarget {
	if !a.planReview.model.zero() {
		return a.planReview.model
	}
	return a.currentTarget()
}

// planTargetWindow is the context window of the model the plan will execute
// on, 0 when unknown.
func (a *App) planTargetWindow() int {
	if m, ok := a.planTargetOffer(); ok {
		return m.ContextWindow
	}
	w, _ := modelinfo.ResolveWith(a.planTarget().model, a.settings.ContextWindows, 0)
	return w
}

// planTargetOffer is the offer's entry for the model the plan will execute
// on, false when the offer does not list it.
func (a *App) planTargetOffer() (webask.PlanModel, bool) {
	t := a.planTarget()
	for _, m := range a.planReview.models {
		if m.Provider == t.provider && m.Model == t.model {
			return m, true
		}
	}
	return webask.PlanModel{}, false
}

// planText is the plan as it will be written into the system prompt.
func (s *planReviewState) planText() string {
	if len(s.doc.Steps) > 0 {
		return s.doc.Render()
	}
	return s.raw
}

// planEstimate estimates each approve route from the last main-turn request
// (its system prompt and tool definitions, and everything it sent) and the
// session's context estimate. The review opens while the planning turn is
// still finishing, before its provider-reported total lands on the transcript,
// so the history is the larger of the two: the context estimate alone would
// leave out the system prompt, the tools and the planning passes' tool results.
func (a *App) planEstimate(planText string) planest.Estimate {
	history := max(a.contextEstimate().Tokens, a.intelReq.lastSent)
	e := planest.Compute(planText, a.intelReq.lastBase, history)
	if e.Here == 0 {
		// Nothing sent yet: both routes send the same.
		e.Here = e.New
	}
	return e
}

// planRouteNote is the estimate shown beside an approve route.
func planRouteNote(tokens, window int) string {
	text := "≈ " + components.FormatTokens(tokens) + " tok"
	pct, fits := planest.Share(tokens, window)
	switch {
	case window <= 0:
		return components.MutedStyle.Render(text)
	case !fits:
		return components.DangerStyle.Render(fmt.Sprintf("%s · won't fit %s", text, components.FormatTokens(window)))
	case pct >= planWarnPct:
		return components.WarnStyle.Render(fmt.Sprintf("%s · %d%% of %s", text, pct, components.FormatTokens(window)))
	case pct == 0:
		return components.MutedStyle.Render(fmt.Sprintf("%s · <1%% of %s", text, components.FormatTokens(window)))
	}
	return components.MutedStyle.Render(fmt.Sprintf("%s · %d%% of %s", text, pct, components.FormatTokens(window)))
}

// planModelLine is the pane's model row, its hints dropped when they would not
// fit width.
func (a *App) planModelLine(width int) string {
	t := a.planTarget()
	line := "model: " + t.label()
	var tags []string
	if t == a.currentTarget() {
		tags = append(tags, "current")
	}
	if m, ok := a.planTargetOffer(); ok && m.Fast {
		tags = append(tags, "fast")
	}
	if w := a.planTargetWindow(); w > 0 {
		tags = append(tags, components.FormatTokens(w)+" window")
	}
	if len(tags) > 0 {
		line += " (" + strings.Join(tags, ", ") + ")"
	}
	for _, hint := range []string{"  ·  m change  ·  approving starts the cache cold", "  ·  m change"} {
		if lipgloss.Width(line+hint) <= width {
			return line + components.MutedStyle.Render(hint)
		}
	}
	return line
}

// planPickMatches is the offer filtered by the picker's text.
func (a *App) planPickMatches() []webask.PlanModel {
	f := strings.ToLower(strings.TrimSpace(a.planReview.pickFilter))
	if f == "" {
		return a.planReview.models
	}
	var out []webask.PlanModel
	for _, m := range a.planReview.models {
		if strings.Contains(strings.ToLower(m.Provider+"/"+m.Model+" "+m.Label), f) {
			out = append(out, m)
		}
	}
	return out
}

// planPickView renders the model list in place of the plan body.
func (a *App) planPickView(height int) string {
	matches := a.planPickMatches()
	var b strings.Builder
	b.WriteString(components.MutedStyle.Render("execute on: " + a.planReview.pickFilter + "▏"))
	b.WriteString("\n")
	rows := height - 1
	if rows < 1 {
		rows = 1
	}
	sel := min(a.planReview.pickSel, max(len(matches)-1, 0))
	start := 0
	if sel >= rows {
		start = sel - rows + 1
	}
	for i := start; i < len(matches) && i < start+rows; i++ {
		m := matches[i]
		line := m.Provider + "/" + m.Model
		var tags []string
		if m.Current {
			tags = append(tags, "current")
		}
		if m.Fast {
			tags = append(tags, "fast")
		}
		if m.ContextWindow > 0 {
			tags = append(tags, components.FormatTokens(m.ContextWindow))
		}
		if len(tags) > 0 {
			line += "  " + components.MutedStyle.Render(strings.Join(tags, " · "))
		}
		if i == sel {
			line = components.EmphStyle.Render("› ") + line
		} else {
			line = "  " + line
		}
		b.WriteString(line + "\n")
	}
	if len(matches) == 0 {
		b.WriteString(components.MutedStyle.Render("  no model matches") + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// handlePlanPickKey drives the model list: type to filter, ↑↓ to move, enter
// to pick, esc to go back to the plan.
func (a *App) handlePlanPickKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := &a.planReview
	switch m.Type {
	case tea.KeyEsc:
		s.picking = false
		return a, nil
	case tea.KeyEnter:
		matches := a.planPickMatches()
		if len(matches) > 0 {
			pick := matches[min(s.pickSel, len(matches)-1)]
			s.model = modelTarget{pick.Provider, pick.Model}
		}
		s.picking = false
		return a, nil
	case tea.KeyUp:
		if s.pickSel > 0 {
			s.pickSel--
		}
		return a, nil
	case tea.KeyDown:
		if s.pickSel < len(a.planPickMatches())-1 {
			s.pickSel++
		}
		return a, nil
	case tea.KeyBackspace:
		if r := []rune(s.pickFilter); len(r) > 0 {
			s.pickFilter = string(r[:len(r)-1])
			s.pickSel = 0
		}
		return a, nil
	case tea.KeyRunes, tea.KeySpace:
		if m.Type == tea.KeySpace {
			s.pickFilter += " "
		} else {
			s.pickFilter += string(m.Runes)
		}
		s.pickSel = 0
		return a, nil
	}
	return a, nil
}

// openPlanPick opens the model list with the plan's model selected.
func (a *App) openPlanPick() {
	s := &a.planReview
	if len(s.models) == 0 {
		s.models = a.planModelOffer()
	}
	s.picking = true
	s.pickFilter = ""
	s.pickSel = 0
	t := a.planTarget()
	for i, m := range s.models {
		if m.Provider == t.provider && m.Model == t.model {
			s.pickSel = i
			break
		}
	}
}

// applyPlanModel switches the session to the model the review picked, before
// the plan executes. A model that cannot be used (no credentials) leaves the
// session on its own model and says so.
func (a *App) applyPlanModel() {
	target := a.planReview.model
	cur := a.currentTarget()
	if target.zero() || target == cur {
		return
	}
	// refreshProvider resends a held prompt when credentials land; the
	// execute turn is submitted by the caller, not from inside the switch.
	held := a.pending
	a.pending = ""
	a.applyModelProvider(target.provider, target.model, "")
	if !a.status.Configured {
		a.applyModelProvider(cur.provider, cur.model, "")
		a.pending = held
		a.addSystem(target.label() + " cannot be used here (no credentials); executing on " + cur.label())
		return
	}
	a.pending = held
	a.normaliseSwap()
	a.addSystem("executing on " + target.label())
}
