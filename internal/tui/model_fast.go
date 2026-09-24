package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/provider"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/tui/components"
)

// The fast tier on the /model screen. routing.fast_model names the model that
// answers the one-token sentinel roles (mode select, session name, goal and
// plan evaluator verdicts); unset, it is the main provider's registry fast
// model. It lives in the routing block, so it shares the routing scope.

// classifierTierOptions are the classifier.tier values the tier row cycles.
var classifierTierOptions = []string{config.ClassifierTierMain, config.ClassifierTierFast}

// fastTarget returns the explicit routing.fast_model, or the zero target.
func (a *App) fastTarget() config.RoutingTarget {
	if a.settings.Routing == nil || a.settings.Routing.Fast == nil {
		return config.RoutingTarget{}
	}
	return *a.settings.Routing.Fast
}

// fastProvider is the provider the fast tier resolves to: the explicit one,
// else the main provider.
func (a *App) fastProvider() string {
	if p := a.fastTarget().Provider; p != "" {
		return p
	}
	return a.cfg.Provider
}

// resolvedFastLabel names the model the fast tier actually runs, or says the
// sentinel roles stay on the main model.
func (a *App) resolvedFastLabel() string {
	if f := a.cfg.Routing.Fast; f != nil {
		return f.Provider + "/" + f.Model
	}
	return "main model (no fast tier for " + a.providerDisplayLabel(a.fastProvider()) + ")"
}

// fastRows builds the FAST TIER group.
func (a *App) fastRows() []modelRow {
	src := sourceLabel(a.eff.Origin["routing"])
	t := a.fastTarget()
	provVal := fmt.Sprintf("— (main: %s)", a.providerDisplayLabel(a.cfg.Provider))
	if t.Provider != "" {
		provVal = a.providerDisplayLabel(t.Provider)
	}
	modelVal := "— (default: " + a.resolvedFastLabel() + ")"
	if t.Model != "" {
		modelVal = t.Model
	} else if d, ok := provider.Lookup(a.fastProvider()); ok && d.FastModel != "" && a.cfg.Routing.Fast == nil {
		modelVal = "— (default: " + d.FastModel + ", not configured)"
	}
	return []modelRow{
		{roleFast, settingsRow{key: "provider", label: "provider", kind: "choose", opts: a.modelProviders(), value: provVal, src: src}},
		{roleFast, settingsRow{key: "model", label: "model", kind: "pick", value: modelVal, src: src}},
	}
}

// classifierTierRow is the security guard's tier toggle.
func (a *App) classifierTierRow() settingsRow {
	val := config.ClassifierTierMain
	if cls := a.settings.Classifier; cls != nil && cls.Tier != "" {
		val = cls.Tier
	}
	return settingsRow{
		key: "tier", label: "tier", kind: "choose",
		opts: classifierTierOptions, value: val,
		src: sourceLabel(a.eff.Origin["classifier"]),
	}
}

// cycleClassifierTier toggles classifier.tier between main and fast.
func (a *App) cycleClassifierTier(opts []string) tea.Cmd {
	cur := config.ClassifierTierMain
	if cls := a.settings.Classifier; cls != nil && cls.Tier != "" {
		cur = cls.Tier
	}
	next := opts[(indexOfString(opts, cur)+1)%len(opts)]
	return a.mutateClassifier(func(c *config.ClassifierSettings) { c.Tier = next })
}

// cycleFastProvider assigns the next provider to the fast tier and clears its
// model, so the provider's own fast model applies until one is picked.
func (a *App) cycleFastProvider(opts []string) tea.Cmd {
	if len(opts) == 0 {
		return nil
	}
	next := opts[(indexOfString(opts, a.fastProvider())+1)%len(opts)]
	return a.mutateRouting(func(r *config.RoutingSettings) {
		r.Fast = &config.RoutingTarget{Provider: next}
	})
}

// openFastModelPicker opens the model picker over the fast tier's provider.
func (a *App) openFastModelPicker() tea.Cmd {
	prov := a.fastProvider()
	a.modelState.picking = true
	a.modelState.pickingRole = roleFast
	a.modelState.filter = ""
	a.modelState.filtering = false
	a.modelState.scroll = 0
	a.modelState.modelIdx = indexOfModel(a.catalogFor(prov), a.fastTarget().Model)
	return a.fetchCatalogCmd(prov)
}

// setFastModel stores a picked fast model, keeping the chosen provider.
func (a *App) setFastModel(id string) tea.Cmd {
	return a.mutateRouting(func(r *config.RoutingSettings) {
		t := config.RoutingTarget{Model: id}
		if r.Fast != nil {
			t.Provider = r.Fast.Provider
		}
		r.Fast = &t
	})
}

// unsetFastRow clears the fast tier's provider (and model with it) or just
// its model. A target left with neither is removed, so the registry default
// applies again.
func (a *App) unsetFastRow(key string) tea.Cmd {
	return a.mutateRouting(func(r *config.RoutingSettings) {
		if r.Fast == nil {
			return
		}
		switch key {
		case "provider":
			r.Fast = nil
			return
		case "model":
			r.Fast.Model = ""
		}
		if r.Fast.Provider == "" && r.Fast.Model == "" {
			r.Fast = nil
		}
	})
}

// modelRoleBlurb is the one-line account of what each /model group decides.
// The routing line follows the routing kind: under "defined" the pool is not
// consulted at all (run.ResolveRouting), and under "routed" Jev picks from the
// whole pool per use case — a pool entry's key is a label, not an assignment.
func (a *App) modelRoleBlurb(role modelRole) string {
	switch role {
	case roleAgent:
		return "does the work: every turn, tool call, compaction, goal contract, clarify and final report"
	case roleFast:
		return "answers one-token verdicts (mode select, session name, goal/plan/agent evaluator) and drafts the goal contract"
	case roleClassifier:
		return "the security guard: classifies the prompt and every arbitrary tool result"
	case roleRouting:
		if a.settings.Routing != nil && a.settings.Routing.Kind == config.RoutingRouted {
			return "Jev picks one model from this pool for each use case; verdicts and the goal contract skip it for the fast tier"
		}
		return "defined: the agent model serves every role; the pool below is unused until kind is routed"
	case rolePosture:
		return "session safety switches; not model settings, so they keep their own save target"
	}
	return ""
}

// modelSummary renders what is in effect, resolved from the live config — the
// part of the page the groups below exist to change. It shares the rows'
// label column so the page reads as one table.
func (a *App) modelSummary(labelW, valW int) string {
	work := a.providerDisplayLabel(a.cfg.Provider) + " · " + a.cfg.Model
	guard := run.GuardConfig(a.cfg)
	guardLabel := a.providerDisplayLabel(guard.Provider) + " · " + guard.Model
	if a.classifierKind() == "models" {
		if a.resolvedSecurityClassifier().Phase3On {
			guardLabel = "local gates + " + guardLabel
		} else {
			guardLabel = "local gates"
		}
	}
	verdicts := a.resolvedFastLabel()
	drafting := "work model"
	if a.cfg.Routing.Kind == config.RoutingRouted && len(a.cfg.Routing.Candidates) > 0 {
		// A fast use case skips Jev whenever a fast tier exists
		// (run.NewRoleClassifier); only without one does Jev pick for it.
		if a.cfg.Routing.Fast == nil {
			verdicts = "Jev picks from pool, else work model"
		}
		drafting = "Jev picks from pool, else work model"
	}
	lines := [][2]string{
		{"work", work},
		{"verdicts", verdicts},
		{"drafting", drafting},
		{"security", guardLabel},
	}
	var b strings.Builder
	b.WriteString(components.EmphStyle.Render("IN EFFECT") + "\n")
	for _, l := range lines {
		b.WriteString("  " + components.MutedStyle.Render(modelIndent+padRight(l[0], labelW)) + truncTail(l[1], valW) + "\n")
	}
	return b.String()
}
