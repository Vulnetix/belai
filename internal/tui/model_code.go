package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/modeltest"
	"github.com/vulnetix/belai/internal/run"
)

// The code-mode model on the /model screen. code.model names the model that
// code-mode turns run on: Smart (the main model, the default), Fast (the fast
// tier), or any provider/model pair. It lives in the code block, and shares
// the routing scope so the page keeps a single save target for the groups
// below the agent.

const (
	codeUseSmart  = config.CodeTierSmart
	codeUseFast   = config.CodeTierFast
	codeUseCustom = "custom"
)

// codeUseOptions are the values the code-mode "use" row cycles.
var codeUseOptions = []string{codeUseSmart, codeUseFast, codeUseCustom}

// codePick returns the configured code.model, or the zero pick.
func (a *App) codePick() config.CodeModel {
	if a.settings.Code == nil || a.settings.Code.Model == nil {
		return config.CodeModel{}
	}
	return *a.settings.Code.Model
}

// codeUse is the row value for the configured pick.
func (a *App) codeUse() string {
	p := a.codePick()
	switch {
	case p.Explicit():
		return codeUseCustom
	case p.Tier == codeUseFast:
		return codeUseFast
	}
	return codeUseSmart
}

// codeProvider is the provider the code pick resolves to: the explicit one,
// else the main provider.
func (a *App) codeProvider() string {
	if p := a.codePick().Provider; p != "" {
		return p
	}
	return a.cfg.Provider
}

// resolvedCodeLabel names the model code-mode turns actually run on.
func (a *App) resolvedCodeLabel() string {
	if c := a.cfg.Routing.Code; c != nil {
		return c.Provider + "/" + c.Model
	}
	if a.codeUse() == codeUseFast {
		return "main model (no fast tier for " + a.providerDisplayLabel(a.cfg.Provider) + ")"
	}
	return "main model"
}

// codeRows builds the CODE MODE group.
func (a *App) codeRows() []modelRow {
	src := sourceLabel(a.eff.Origin["code"])
	use := a.codeUse()
	rows := []modelRow{
		{roleCode, settingsRow{key: "use", label: "use", kind: "choose", opts: codeUseOptions, value: use, src: src,
			help: "smart = the main model · fast = the fast tier · custom = any available provider"}},
	}
	p := a.codePick()
	provVal := fmt.Sprintf("— (main: %s)", a.providerDisplayLabel(a.cfg.Provider))
	if p.Provider != "" {
		provVal = a.providerDisplayLabel(p.Provider)
	}
	modelVal := "— (default: " + a.resolvedCodeLabel() + ")"
	if p.Model != "" {
		modelVal = p.Model
	}
	custom := use == codeUseCustom
	rows = append(rows,
		modelRow{roleCode, settingsRow{key: "provider", label: "provider", kind: "choose", opts: a.modelProviders(), value: provVal, src: src, disabled: !custom}},
		modelRow{roleCode, settingsRow{key: "model", label: "model", kind: "pick", value: modelVal, src: src, disabled: !custom}},
	)
	return rows
}

// cycleCodeUse steps the use row. Smart and fast are stored explicitly, so a
// project can override a global custom pick without leaving it unset.
func (a *App) cycleCodeUse(opts []string) tea.Cmd {
	next := opts[(indexOfString(opts, a.codeUse())+1)%len(opts)]
	switch next {
	case codeUseCustom:
		return a.openProviderPicker(roleCode, a.modelProviders(), a.codeProvider())
	default:
		return a.stageCode("use", "code mode = "+next, func(c *config.CodeSettings) {
			c.Model = &config.CodeModel{Tier: next}
		})
	}
}

// openCodeModelPicker opens the model picker over the code pick's provider.
func (a *App) openCodeModelPicker() tea.Cmd {
	prov := a.pickerProvider(roleCode)
	a.modelState.picking = true
	a.modelState.pickingRole = roleCode
	a.modelState.filter = ""
	a.modelState.filtering = false
	a.modelState.scroll = 0
	a.modelState.modelIdx = indexOfModel(a.catalogFor(prov), a.codePick().Model)
	return a.fetchCatalogCmd(prov)
}

// setCodePick stores an explicit provider/model pick.
func (a *App) setCodePick(prov, id string) tea.Cmd {
	return a.stageCode("model", "code mode = "+prov+" · "+id, func(c *config.CodeSettings) {
		c.Model = &config.CodeModel{Provider: prov, Model: id}
	})
}

// unsetCodeRow clears the pick, so code mode runs on Smart again. The group
// can only be cleared whole: a half-set pair has no meaning.
func (a *App) unsetCodeRow() tea.Cmd {
	return a.mutateCode(func(c *config.CodeSettings) { c.Model = nil })
}

// stageCode tests the code-mode model and writes it on a pass.
func (a *App) stageCode(rowKey, label string, fn func(*config.CodeSettings)) tea.Cmd {
	cand := cloneSettings(a.settings)
	if cand.Code == nil {
		cand.Code = &config.CodeSettings{}
	}
	fn(cand.Code)
	ch := stagedChange{
		role: roleCode, rowKey: rowKey, label: label, was: "the current code-mode model",
		write:   func() tea.Cmd { return a.mutateCode(fn) },
		scope:   a.modelState.routingScope,
		restage: func() tea.Cmd { return a.stageCode(rowKey, label, fn) },
	}
	if err := config.ValidateCodeModel(cand); err != nil {
		return a.failStaged(ch, "validate", err.Error())
	}
	rc, err := run.ResolveRouting(a.cfg, a.settings.Routing, a.credSource())
	if err != nil {
		return a.failStaged(ch, "resolve", err.Error())
	}
	code, err := run.ResolveCode(a.cfg, cand.Code, rc.Fast, a.credSource())
	if err != nil {
		return a.failStaged(ch, "resolve", err.Error())
	}
	var t modeltest.Target
	if code != nil {
		t.Chats = append(t.Chats, a.chatTarget("code", code.Provider, code.Model, "none", false))
	}
	t.Chats = a.freshChats(t.Chats)
	ch.plan(t)
	return a.startModelTest(ch)
}

// mutateCode applies fn to the code block in the page's scope, reloads the
// merged settings, and re-resolves the routing config.
func (a *App) mutateCode(fn func(*config.CodeSettings)) tea.Cmd {
	scope := a.modelState.routingScope
	cfgScope := config.ScopeProject
	if scope == "global" {
		cfgScope = config.ScopeGlobal
	}
	if err := config.Mutate(cfgScope, a.workdir, func(s *config.Settings) error {
		if s.Code == nil {
			s.Code = &config.CodeSettings{}
		}
		fn(s.Code)
		return nil
	}); err != nil {
		a.modelState.errorMsg = err.Error()
		return nil
	}
	if err := a.reloadSettings(); err != nil {
		a.modelState.errorMsg = err.Error()
		return nil
	}
	a.modelState.errorMsg = ""
	return a.refreshProvider()
}
