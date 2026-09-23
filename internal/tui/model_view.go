package tui

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/mlclassify"
	"github.com/vulnetix/signet/internal/modelfetch"
	"github.com/vulnetix/signet/internal/models"
	"github.com/vulnetix/signet/internal/provider"
	"github.com/vulnetix/signet/internal/rolemanager/jev"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/tui/components"
)

// modelRole selects which role is being edited on the /model screen.
type modelRole string

const (
	roleAgent      modelRole = "agent"
	roleClassifier modelRole = "classifier"
)

// modelViewState tracks the /model role screen.
type modelViewState struct {
	selected int // row within modelRows()
	rows     []modelRow

	agentScope      string // session | global | project
	classifierScope string // global | project

	// classifierLastEffort preserves the classifier effort chip across
	// reasoning off/on; agentLastEffort does the same for the main model.
	classifierLastEffort string
	agentLastEffort      string

	// sub-picker state, shared by agent and classifier model rows.
	picking     bool
	pickingRole modelRole
	modelIdx    int
	filter      string
	filtering   bool
	scroll      int

	errorMsg string
}

func (a *App) enterModel() tea.Cmd {
	agentScope := a.modelState.agentScope
	if agentScope == "" {
		agentScope = "session"
	}
	clsScope := a.modelState.classifierScope
	if clsScope == "" {
		clsScope = "project"
	}
	last := a.modelState.classifierLastEffort
	if cls := a.settings.Classifier; cls != nil && reasoningEffort(cls.Effort) {
		last = cls.Effort
	}
	if last == "" {
		last = "medium"
	}
	agentLast := a.settings.Effort
	if agentLast == "none" {
		agentLast = ""
	}
	a.modelState = modelViewState{agentScope: agentScope, classifierScope: clsScope, classifierLastEffort: last, agentLastEffort: agentLast}
	return tea.Batch(a.fetchCatalogCmd(a.cfg.Provider), a.availabilityCmdIfStale())
}

// modelsFetchedMsg carries the result of an async live-catalogue fetch.
type modelsFetchedMsg struct {
	provider string
	models   []models.Model
	err      error
}

// catalogTarget resolves the modelfetch target for a provider's live model
// catalogue. The Cloudflare AI Gateway has no model-list endpoint of its own:
// its catalogue is the account's Workers AI model list, fetched with the
// Workers AI credentials (the Cloudflare API token) rather than the gateway
// token used for inference. When Workers AI credentials are absent the target
// falls back to the gateway name so the picker degrades to the static
// catalogue.
func catalogTarget(name string, src run.CredentialSource) modelfetch.Target {
	cfg, _ := run.Prepare("", name, src)
	target := modelfetch.Target{Name: name, BaseURL: cfg.BaseURL, APIKey: cfg.APIKey, Auth: cfg.Auth, API: cfg.API, Kind: cfg.Kind}
	if name == "cloudflare-ai-gateway" {
		if wcfg, wstatus := run.Prepare("", "cloudflare-workers-ai", src); wstatus.Configured {
			target = modelfetch.Target{Name: "cloudflare-workers-ai", BaseURL: wcfg.BaseURL, APIKey: wcfg.APIKey, Auth: wcfg.Auth, API: wcfg.API}
		}
	}
	return target
}

// fetchCatalogCmd fetches the live model catalogue for a provider off the UI
// thread, caching the result.
func (a *App) fetchCatalogCmd(name string) tea.Cmd {
	if a.catalogCache == nil {
		a.catalogCache = map[string][]models.Model{}
	}
	if a.catalogLoading == nil {
		a.catalogLoading = map[string]bool{}
	}
	if a.catalogURLs == nil {
		a.catalogURLs = map[string]string{}
	}
	if _, ok := a.catalogCache[name]; ok || a.catalogLoading[name] {
		return nil
	}

	src := run.CredentialSource(run.EnvSource(os.Getenv))
	if a.resolver != nil {
		src = a.resolver
	}
	// Resolve the target provider (not the currently-committed one) so
	// browsing previewed providers still fetches from the right endpoint.
	target := catalogTarget(name, src)
	endpoint, err := modelfetch.EndpointFor(target)
	if err != nil {
		// Unresolvable target (e.g. gateway without account_id): surface the
		// error on the picker rather than pretending a fetch is in flight.
		return func() tea.Msg { return modelsFetchedMsg{provider: name, err: err} }
	}
	if endpoint == "" {
		return nil // static-only: catalogue comes from the curated list/profile
	}

	return a.startCatalogFetch(name, target, endpoint)
}

// startCatalogFetch marks the provider as in flight and returns the command
// that performs the HTTP fetch. Both the picker's fetch and the startup
// prefetch funnel through here so the loading flag, the displayed URL and the
// resulting message are identical either way.
func (a *App) startCatalogFetch(name string, target modelfetch.Target, endpoint string) tea.Cmd {
	a.catalogLoading[name] = true
	a.catalogURLs[name] = endpoint
	client := a.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		m, err := modelfetch.List(ctx, target, client)
		return modelsFetchedMsg{provider: name, models: m, err: err}
	}
}

// catalogTargetMsg carries a background-resolved fetch target back to the UI
// thread.
type catalogTargetMsg struct {
	provider string
	target   modelfetch.Target
	endpoint string
	err      error
}

// prefetchCatalogCmd warms a provider's live catalogue without opening the
// model picker. The footer's context meter needs the selected model's context
// window, which only the live catalogue carries for most providers, so the TUI
// fetches it on startup. Credential resolution can probe the host keychain, so
// it happens inside the command rather than on the UI thread; the resolved
// target comes back as a catalogTargetMsg.
func (a *App) prefetchCatalogCmd(name string) tea.Cmd {
	if name == "" {
		return nil
	}
	if _, ok := a.catalogCache[name]; ok || a.catalogLoading[name] {
		return nil
	}
	src := run.CredentialSource(run.EnvSource(os.Getenv))
	if a.resolver != nil {
		src = a.resolver
	}
	return func() tea.Msg {
		target := catalogTarget(name, src)
		endpoint, err := modelfetch.EndpointFor(target)
		return catalogTargetMsg{provider: name, target: target, endpoint: endpoint, err: err}
	}
}

// handleCatalogTarget starts the prefetch for a resolved target. It is silent:
// an unresolvable target or a static-only provider leaves the transcript
// untouched, and the error is recorded for the picker to show.
func (a *App) handleCatalogTarget(m catalogTargetMsg) tea.Cmd {
	if m.err != nil {
		if a.catalogErr == nil {
			a.catalogErr = map[string]string{}
		}
		a.catalogErr[m.provider] = m.err.Error()
		return nil
	}
	if m.endpoint == "" {
		return nil // static-only: the curated list already covers it
	}
	if _, ok := a.catalogCache[m.provider]; ok || a.catalogLoading[m.provider] {
		return nil
	}
	if a.catalogLoading == nil {
		a.catalogLoading = map[string]bool{}
	}
	if a.catalogURLs == nil {
		a.catalogURLs = map[string]string{}
	}
	return a.startCatalogFetch(m.provider, m.target, m.endpoint)
}

// handleModelsFetched fills the catalogue cache.
func (a *App) handleModelsFetched(m modelsFetchedMsg) tea.Cmd {
	if a.catalogCache == nil {
		a.catalogCache = map[string][]models.Model{}
	}
	if a.catalogErr == nil {
		a.catalogErr = map[string]string{}
	}
	delete(a.catalogLoading, m.provider)
	delete(a.catalogURLs, m.provider)
	if m.err != nil {
		a.catalogErr[m.provider] = m.err.Error()
		return nil
	}
	delete(a.catalogErr, m.provider)
	a.catalogCache[m.provider] = m.models
	// The catalogue carries the context window the footer meter scales to,
	// so the footer must re-read it the moment a fetch lands.
	a.refreshFooter()
	return nil
}

// filterModels narrows a catalogue by a case-insensitive substring match on
// the model id. An empty filter returns the catalogue unchanged. It mirrors
// agentCandidates (agentpick.go): the picker's index refers to the filtered
// slice, so every consumer must share one filtering function.
func filterModels(catalog []models.Model, q string) []models.Model {
	if q == "" {
		return catalog
	}
	lower := strings.ToLower(q)
	var out []models.Model
	for _, m := range catalog {
		if strings.Contains(strings.ToLower(m.ID), lower) {
			out = append(out, m)
		}
	}
	return out
}

// modelCatalog resolves the current provider name and its filtered catalogue.
// Render, cursor movement and commit must all read this so the model index
// always refers to the same (filtered) slice — commitModel indexing an
// unfiltered catalogue would set the wrong model whenever a filter is active.
// modelProviders is the provider list the picker offers: only those actually
// usable, plus the committed provider so a picker can never silently move the
// user off their own model. Every site in this view reads it, so the tab
// strip, the cursor and commitModel always index the same slice.
func (a *App) modelProviders() []string {
	return a.availableProviders(a.cfg.Provider)
}

// windowStart clamps off so the cursor stays inside a rows-tall window over n
// items. rows >= n means everything fits and the window starts at 0; when the
// cursor is above the window the window jumps up to it, and when it is below
// the window the window slides down so the cursor sits on the last visible
// row. The result is finally clamped to [0, n-rows]. The up/down wrap-around
// needs no special case: jumping 0 -> n-1 trips the below rule and n-1 -> 0
// trips the above rule.
func windowStart(off, cursor, n, rows int) int {
	if rows >= n {
		return 0
	}
	start := off
	if off > cursor {
		start = cursor
	}
	if cursor >= off+rows {
		start = cursor - rows + 1
	}
	if start < 0 {
		start = 0
	}
	if start > n-rows {
		start = n - rows
	}
	return start
}

// modelSearchLine renders the filter box above the list: a muted hint when
// idle and unfiltered, the active filter with an `esc clears` hint when a
// filter is accepted, and the accented box with a block cursor while focused.
func (a *App) modelSearchLine() string {
	const label = "search  "
	switch {
	case a.modelState.filtering:
		return components.AccentStyle.Render(label + a.modelState.filter + "▌")
	case a.modelState.filter != "":
		return components.MutedStyle.Render(label) +
			components.EmphStyle.Render(a.modelState.filter) +
			components.MutedStyle.Render("  esc clears")
	default:
		return components.MutedStyle.Render(label + "/ to filter")
	}
}

// modelRow is one editable row belonging to a role.
type modelRow struct {
	role modelRole
	settingsRow
}

var defaultModelEfforts = []string{"low", "medium", "high"}

// classifierThresholdOptions are the attack-probability thresholds the phase
// threshold rows cycle through. They are written back to
// classifier.phaseN.threshold as numeric values.
var classifierThresholdOptions = []string{"0.50", "0.60", "0.70", "0.75", "0.80", "0.85", "0.90", "0.95"}

// scopeOptions lists the storage scopes each role may cycle through. The agent
// role can stay session-only; the classifier is global or project only.
var (
	agentScopeOptions      = []string{"session", "global", "project"}
	classifierScopeOptions = []string{"global", "project"}
)

// modelRows builds the declarative row table for both roles.
func (a *App) modelRows() []modelRow {
	origin := a.eff.Origin
	var rows []modelRow

	agentOn := a.settings.Effort != "none"
	agentEffortVal := a.settings.Effort
	if !agentOn {
		agentEffortVal = "none (reasoning off)"
	} else if agentEffortVal == "" {
		agentEffortVal = "—"
	}

	// Agent role — the global model settings.
	rows = append(rows, modelRow{roleAgent, settingsRow{
		key: "provider", label: "provider", kind: "choose",
		opts: a.modelProviders(), value: a.providerDisplayLabel(a.cfg.Provider),
		src: sourceLabel(origin["provider"]),
	}})
	rows = append(rows, modelRow{roleAgent, settingsRow{
		key: "model", label: "model", kind: "pick",
		value: a.cfg.Model, src: sourceLabel(origin["model"]),
	}})
	rows = append(rows, modelRow{roleAgent, settingsRow{
		key: "effort", label: "effort", kind: "choose",
		opts: a.agentEffortOpts(), value: agentEffortVal,
		src:      sourceLabel(origin["effort"]),
		disabled: !agentOn,
	}})
	rows = append(rows, modelRow{roleAgent, settingsRow{
		key: "reasoning", label: "reasoning", kind: "toggle",
		value: boolLabel(agentOn), src: sourceLabel(origin["effort"]),
	}})
	rows = append(rows, modelRow{roleAgent, settingsRow{
		key: "caveman", label: "caveman", kind: "toggle",
		value: boolLabel(a.settings.CavemanEnabled()), src: sourceLabel(origin["caveman"]),
	}})
	rows = append(rows, modelRow{roleAgent, settingsRow{
		key: "guardrails", label: "guardrails", kind: "toggle",
		value: boolLabel(a.guardrailsEnabled()), src: sourceLabel(origin["guardrails"]),
	}})
	rows = append(rows, modelRow{roleAgent, settingsRow{
		key: "ask", label: "ask", kind: "toggle",
		value: boolLabel(a.askEnabled()), src: sourceLabel(origin["ask_permission"]),
	}})
	rows = append(rows, modelRow{roleAgent, settingsRow{
		key: "firewall", label: "firewall", kind: "toggle",
		value: boolLabel(a.firewallEnabled()), src: sourceLabel(origin["firewall_enabled"]),
	}})
	rows = append(rows, modelRow{roleAgent, settingsRow{
		key: "scope", label: "scope", kind: "choose",
		opts:  agentScopeOptions,
		value: a.modelState.agentScope,
	}})

	// Classifier role.
	cls := a.settings.Classifier
	src := sourceLabel(origin["classifier"])
	providerVal := fmt.Sprintf("— (main: %s)", a.providerDisplayLabel(a.cfg.Provider))
	if cls != nil && cls.Provider != "" {
		providerVal = a.providerDisplayLabel(cls.Provider)
	}
	modelVal := fmt.Sprintf("— (main: %s)", a.cfg.Model)
	if cls != nil && cls.Model != "" {
		modelVal = cls.Model
	}
	on := cls != nil && reasoningEffort(cls.Effort)
	effortVal := "none (reasoning off)"
	if on {
		effortVal = cls.Effort
	}
	var chunkVal string
	if cls != nil {
		c := cls.Chunk
		chunkVal = fmt.Sprintf("%s ×%d", humanizeBytes(c.MaxBytesOr()), c.ConcurrencyOr())
	} else {
		chunkVal = fmt.Sprintf("%s ×%d", humanizeBytes(config.ClassifierChunkSettings{}.MaxBytesOr()), config.ClassifierChunkSettings{}.ConcurrencyOr())
	}
	clsScope := a.modelState.classifierScope
	if clsScope == "" {
		clsScope = "project"
	}

	kind := a.classifierKind()
	kindRow := settingsRow{
		key: "kind", label: "kind", kind: "choose",
		opts: []string{"llm", "models"}, value: kind, src: src,
		disabled: mlclassify.Embedded(),
	}

	rows = append(rows, modelRow{roleClassifier, kindRow})
	rows = append(rows, modelRow{roleClassifier, settingsRow{
		key: "provider", label: "provider", kind: "choose",
		opts: a.classifierProviders(), value: providerVal, src: src,
	}})
	rows = append(rows, modelRow{roleClassifier, settingsRow{
		key: "model", label: "model", kind: "pick",
		value: modelVal, src: src,
	}})
	if kind == "models" {
		rows = append(rows, modelRow{roleClassifier, a.classifierPhaseRow(1)})
		rows = append(rows, modelRow{roleClassifier, a.classifierPhaseThresholdRow(1)})
		rows = append(rows, modelRow{roleClassifier, a.classifierPhaseRow(2)})
		rows = append(rows, modelRow{roleClassifier, a.classifierPhaseThresholdRow(2)})
		rows = append(rows, modelRow{roleClassifier, a.classifierPhase3Row()})
	}
	rows = append(rows, modelRow{roleClassifier, settingsRow{
		key: "reasoning", label: "reasoning", kind: "toggle",
		value: boolLabel(on), src: src,
	}})
	rows = append(rows, modelRow{roleClassifier, settingsRow{
		key: "effort", label: "effort", kind: "choose",
		opts: a.classifierEffortOpts(), value: effortVal, src: src,
		disabled: !on,
	}})
	rows = append(rows, modelRow{roleClassifier, settingsRow{
		key: "chunk", label: "chunk", kind: "text",
		value: chunkVal, src: src,
	}})
	rows = append(rows, modelRow{roleClassifier, settingsRow{
		key: "scope", label: "scope", kind: "choose",
		opts:  classifierScopeOptions,
		value: clsScope,
	}})

	return rows
}

func safeRow(rows []modelRow, i int) modelRow {
	if i < 0 || i >= len(rows) {
		return modelRow{roleAgent, settingsRow{key: "", kind: "text"}}
	}
	return rows[i]
}

// scopeTarget returns the human-readable storage path for a scope badge.
func (a *App) scopeTarget(scope string) string {
	switch scope {
	case "global":
		if p, _ := config.GlobalSettingsPath(); p != "" {
			return p
		}
	case "session":
		return "(session only)"
	}
	return config.ProjectSettingsPath(a.workdir)
}

// modelGroupHeader renders a labelled role group with that role's own scope
// badge, so moving the cursor between roles cannot rewrite the header.
func (a *App) modelGroupHeader(role modelRole) string {
	var scope string
	switch role {
	case roleAgent:
		scope = a.modelState.agentScope
		if scope == "" {
			scope = "session"
		}
	case roleClassifier:
		scope = a.modelState.classifierScope
		if scope == "" {
			scope = "project"
		}
	}
	name := strings.ToUpper(string(role))
	b := strings.Builder{}
	b.WriteString(name)
	b.WriteString("   ")
	b.WriteString(components.Chip(scope, components.ColorTealSoft))
	b.WriteString("  ")
	b.WriteString(components.MutedStyle.Render(a.scopeTarget(scope)))
	if role == roleClassifier {
		b.WriteString("\n")
		b.WriteString(components.WarnStyle.Render(
			"! the classifier is the security gate for tool output; a weaker model means weaker detection"))
	}
	return b.String()
}

func (a *App) modelView() string {
	w := a.contentWidth()
	rows := a.modelRows()
	a.modelState.rows = rows

	var b strings.Builder
	b.WriteString(components.SectionHeader("Model Roles", "esc back", w))

	if a.modelState.picking {
		b.WriteString("\n")
		b.WriteString(a.modelPicker())
		return lipgloss.NewStyle().Padding(1).Render(b.String())
	}

	var prev modelRole
	for i, r := range rows {
		if r.role != prev {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(a.modelGroupHeader(r.role) + "\n")
			prev = r.role
		}
		selected := i == a.modelState.selected
		label := fmt.Sprintf("  %-12s", r.label)
		value := ansi.Truncate(r.value+" ", 41, "…")
		switch {
		case r.disabled:
			label = components.MutedStyle.Render(label)
			value = components.MutedStyle.Render(value)
		case selected:
			label = components.AccentStyle.Bold(true).Render(label)
			value = components.EmphStyle.Render(value)
		default:
			label = components.MutedStyle.Render(label)
		}
		b.WriteString(components.Cursor(selected) + label + value +
			components.MutedStyle.Render(r.src) + "\n")
	}

	if a.avail.note != "" {
		b.WriteString("\n" + components.MutedStyle.Render(a.avail.note) + "\n")
	}
	if a.modelState.errorMsg != "" {
		b.WriteString("\n" + components.DangerStyle.Render("✗ "+a.modelState.errorMsg) + "\n")
	}
	b.WriteString("\n" + components.HelpBar(
		"↑↓", "move", "⏎", "edit", "s", "scope", "c", "clear", "p", "providers", "esc", "back") + "\n")
	return lipgloss.NewStyle().Padding(1).Render(b.String())
}

// modelPicker renders the embedded model sub-picker.
func (a *App) modelPicker() string {
	name, catalog := a.modelPickerCatalog()
	var b strings.Builder
	b.WriteString(components.MutedStyle.Render("model for ") + components.Chip(name, components.ColorTeal) + "\n")
	if a.modelState.pickingRole == roleClassifier && a.classifierProviderNeedsWarning(name) {
		b.WriteString(components.WarnStyle.Render(
			"Classifier provider: choose a classifier-specific model or switch to kind LLM for general chat models.") + "\n")
	}
	b.WriteString(a.modelSearchLine() + "\n")

	const rows = 10
	if len(catalog) == 0 {
		b.WriteString("\n" + components.MutedStyle.Render("  no catalogue for this provider — esc to cancel") + "\n")
	} else {
		midx := clampIdx(a.modelState.modelIdx, len(catalog))
		start := windowStart(a.modelState.scroll, midx, len(catalog), rows)
		a.modelState.scroll = start
		end := min(start+rows, len(catalog))
		for i := start; i < end; i++ {
			selected := i == midx
			line := fmt.Sprintf("%-40s", catalog[i].ID)
			if selected {
				line = components.EmphStyle.Render(line)
			} else {
				line = components.MutedStyle.Render(line)
			}
			// Curated classifier models carry a brief efficacy/benefit note that
			// renders to the right of the id, subtly muted so the id stays the
			// prominent column.
			if catalog[i].Label != "" {
				line += "  " + components.MutedStyle.Render(catalog[i].Label)
			}
			b.WriteString(components.Cursor(selected) + line + "\n")
		}
		b.WriteString(components.MutedStyle.Render(fmt.Sprintf("  %d/%d", midx+1, len(catalog))) + "\n")
	}

	if a.modelState.filtering {
		b.WriteString("\n" + components.HelpBar("type", "filter", "enter", "accept", "esc", "clear") + "\n")
	} else {
		b.WriteString("\n" + components.HelpBar("↑↓", "model", "/", "filter", "enter", "set", "esc", "cancel") + "\n")
	}
	return b.String()
}

func (a *App) modelPickerCatalog() (string, []models.Model) {
	var name string
	switch a.modelState.pickingRole {
	case roleAgent:
		name = a.cfg.Provider
		if name == "" {
			if providers := a.modelProviders(); len(providers) > 0 {
				name = providers[0]
			}
		}
	case roleClassifier:
		name = a.classifierProvider()
	}
	if name == "" {
		return name, nil
	}
	catalog := a.catalogFor(name)
	// The classifier-only filter (curated BERT ids on huggingface, Jev on
	// openrouter) applies to the models path, where the picker offers
	// classifier-appropriate models. On the llm path the classifier is the LLM
	// sentinel, so the provider's full chat catalogue stays selectable.
	if a.modelState.pickingRole == roleClassifier && a.classifierKind() == "models" {
		catalog = a.classifierCatalogFor(name, catalog)
	}
	return name, filterModels(catalog, a.modelState.filter)
}

// classifierCatalogFor restricts a provider's catalogue to the models the
// classifier role may pick on the models path. It is a UX filter, not a
// security boundary: run.Prepare remains the fail-closed gate on actually
// using a model.
func (a *App) classifierCatalogFor(providerName string, catalog []models.Model) []models.Model {
	switch providerName {
	case "huggingface":
		// The five curated BERT classifier ids are seeded first so the picker
		// is never empty (huggingface has no static chat catalogue), then any
		// matching entries the live catalogue happens to return. Each seeded
		// model carries its efficacy/benefit blurb for the picker's right
		// column.
		out := make([]models.Model, 0, len(mlclassify.ClassifierModelIDs()))
		for _, id := range mlclassify.ClassifierModelIDs() {
			label, _ := mlclassify.BlurbFor(id)
			out = append(out, models.Model{ID: id, Label: label})
		}
		for _, m := range catalog {
			if mlclassify.IsKnownClassifierModel(m.ID) && indexOfModel(out, m.ID) < 0 {
				out = append(out, m)
			}
		}
		return out
	case "openrouter":
		// Jev is a Decisions API model, not a chat model, so it never appears in
		// the live chat model list. Seed the known Jev model id so the
		// classifier picker always offers it, then keep any additional
		// typesafe/jev* ids the catalogue happens to return.
		out := []models.Model{{ID: jev.DefaultModel, Label: "tool-call gatekeeper · probability verdicts"}}
		for _, m := range catalog {
			if strings.HasPrefix(m.ID, "typesafe/jev") && m.ID != jev.DefaultModel {
				out = append(out, m)
			}
		}
		return out
	default:
		// Custom providers, llama-server and ollama are broad-model
		// providers: every model stays selectable and the picker shows the
		// classifier-specific warning instead of filtering.
		return catalog
	}
}

// classifierProviderNeedsWarning reports whether the classifier model picker
// must show the broad-model warning for a provider: custom profiles and the
// built-in local servers can serve any model, so the user must choose a
// classifier-appropriate one themselves.
func (a *App) classifierProviderNeedsWarning(name string) bool {
	switch name {
	case "llama-server", "ollama":
		return true
	}
	return name != "" && !provider.Builtin(name)
}

func (a *App) handleModelKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.modelState.picking {
		return a.handleModelPickerKey(m)
	}
	rows := a.modelRows()
	a.modelState.rows = rows

	switch m.String() {
	case "esc":
		a.pop()
		return a, nil
	case "up", "k":
		if a.modelState.selected > 0 {
			a.modelState.selected--
		}
		return a, nil
	case "down", "j":
		if a.modelState.selected < len(rows)-1 {
			a.modelState.selected++
		}
		return a, nil
	case "p":
		return a, a.push(viewProviders)
	case "s":
		return a, a.cycleScope()
	case "c", "x":
		return a, a.unsetModelRow()
	case " ", "enter":
		return a, a.changeModelRow()
	}
	return a, nil
}

func (a *App) handleModelPickerKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.modelState.filtering {
		switch m.Type {
		case tea.KeyEsc:
			a.modelState.filtering = false
			a.modelState.filter = ""
			a.modelState.modelIdx = 0
			a.modelState.scroll = 0
		case tea.KeyEnter:
			a.modelState.filtering = false
		case tea.KeyRunes:
			a.modelState.filter += string(m.Runes)
			a.modelState.modelIdx = 0
			a.modelState.scroll = 0
		case tea.KeySpace:
			a.modelState.filter += " "
			a.modelState.modelIdx = 0
			a.modelState.scroll = 0
		case tea.KeyBackspace:
			a.modelState.filter = trimLastRune(a.modelState.filter)
			a.modelState.modelIdx = 0
			a.modelState.scroll = 0
		}
		return a, nil
	}

	_, catalog := a.modelPickerCatalog()
	switch m.String() {
	case "esc":
		a.modelState.picking = false
		a.modelState.filter = ""
		return a, nil
	case "/":
		a.modelState.filtering = true
		return a, nil
	case "up", "k":
		if a.modelState.modelIdx > 0 {
			a.modelState.modelIdx--
		} else if len(catalog) > 0 {
			a.modelState.modelIdx = len(catalog) - 1
		}
		return a, nil
	case "down", "j":
		if len(catalog) > 0 && a.modelState.modelIdx < len(catalog)-1 {
			a.modelState.modelIdx++
		} else {
			a.modelState.modelIdx = 0
		}
		return a, nil
	case " ", "enter":
		if len(catalog) == 0 {
			a.modelState.picking = false
			return a, nil
		}
		id := catalog[clampIdx(a.modelState.modelIdx, len(catalog))].ID
		a.modelState.picking = false
		a.modelState.filter = ""
		switch a.modelState.pickingRole {
		case roleAgent:
			return a, a.mutateAgent(func(s *config.Settings) { s.Model = id }, func() { a.cfg.Model = id })
		case roleClassifier:
			return a, a.mutateClassifier(func(c *config.ClassifierSettings) { c.Model = id })
		}
	}
	return a, nil
}

// cycleScope advances the storage scope for the selected role. It reads the
// role's own scope options, never the selected row's options: the row is
// whatever the cursor happens to be on (provider, model, effort, …), and its
// opts are provider names or effort chips, not scopes. Using them here let a
// press of `s` on the provider row write a provider name into the scope field.
func (a *App) cycleScope() tea.Cmd {
	row := safeRow(a.modelState.rows, a.modelState.selected)
	var opts []string
	var cur string
	switch row.role {
	case roleAgent:
		opts = agentScopeOptions
		cur = a.modelState.agentScope
	case roleClassifier:
		opts = classifierScopeOptions
		cur = a.modelState.classifierScope
	}
	next := opts[(indexOfString(opts, cur)+1)%len(opts)]
	switch row.role {
	case roleAgent:
		a.modelState.agentScope = next
	case roleClassifier:
		a.modelState.classifierScope = next
	}
	return nil
}

func (a *App) changeModelRow() tea.Cmd {
	row := safeRow(a.modelState.rows, a.modelState.selected)
	if row.disabled {
		return nil
	}
	switch row.key {
	case "kind":
		return a.cycleClassifierKind(row.opts)
	case "phase1":
		return a.cycleClassifierPhase(1, row.opts)
	case "phase2":
		return a.cycleClassifierPhase(2, row.opts)
	case "phase1-threshold":
		return a.cycleClassifierPhaseThreshold(1, row.opts)
	case "phase2-threshold":
		return a.cycleClassifierPhaseThreshold(2, row.opts)
	case "provider":
		if row.role == roleAgent {
			return a.cycleAgentProvider(row.opts)
		}
		return a.cycleClassifierProvider(row.opts)
	case "model":
		if row.role == roleAgent {
			return a.openAgentModelPicker()
		}
		return a.openClassifierModelPicker()
	case "effort":
		if row.role == roleAgent {
			return a.cycleAgentEffort(row.opts)
		}
		return a.cycleClassifierEffort(row.opts)
	case "reasoning":
		if row.role == roleAgent {
			return a.toggleAgentReasoning()
		}
		return a.toggleClassifierReasoning()
	case "caveman":
		return a.toggleCaveman()
	case "guardrails":
		return a.toggleGuardrails()
	case "ask":
		return a.toggleAsk()
	case "firewall":
		return a.toggleFirewall()
	case "scope":
		return a.cycleScope()
	}
	return nil
}

func (a *App) unsetModelRow() tea.Cmd {
	row := safeRow(a.modelState.rows, a.modelState.selected)
	switch row.role {
	case roleAgent:
		switch row.key {
		case "model":
			return a.mutateAgent(func(s *config.Settings) { s.Model = "" }, func() { a.cfg.Model = "" })
		case "effort":
			return a.mutateAgent(func(s *config.Settings) { s.Effort = "" }, func() {
				a.cfg.Effort = ""
				a.settings.Effort = ""
			})
		case "provider":
			return a.mutateAgent(func(s *config.Settings) {
				s.Provider = ""
				s.Model = ""
				s.Effort = ""
			}, func() {
				a.cfg.Provider = ""
				a.cfg.Model = ""
				a.cfg.Effort = ""
				a.settings.Effort = ""
			})
		case "reasoning":
			return a.mutateAgent(func(s *config.Settings) { s.Effort = "" }, func() {
				a.cfg.Effort = ""
				a.settings.Effort = ""
			})
		case "caveman":
			return a.clearCaveman()
		case "guardrails":
			return a.clearGuardrails()
		case "ask":
			return a.clearAsk()
		case "firewall":
			return a.clearFirewall()
		}
	case roleClassifier:
		return a.unsetClassifierRow(row.key)
	}
	return nil
}

// cycleClassifierKind toggles the classifier stack between llm and models.
func (a *App) cycleClassifierKind(opts []string) tea.Cmd {
	if len(opts) == 0 {
		return nil
	}
	next := opts[(indexOfString(opts, a.classifierKind())+1)%len(opts)]
	return a.mutateClassifier(func(c *config.ClassifierSettings) { c.Kind = next })
}

// cycleClassifierPhase advances one phase gate's source through the choices the
// row offered. The value committed is a source token ("embedded",
// "huggingface", or "disabled" for phase 2).
func (a *App) cycleClassifierPhase(phase int, opts []string) tea.Cmd {
	if len(opts) == 0 {
		return nil
	}
	cur := a.classifierPhaseSource(phase)
	next := opts[(indexOfString(opts, cur)+1)%len(opts)]
	return a.mutateClassifier(func(c *config.ClassifierSettings) {
		if phase == 1 {
			c.Phase1.Source = next
		} else {
			c.Phase2.Source = next
		}
	})
}

// cycleClassifierPhaseThreshold advances one phase gate's attack threshold
// through the choices the threshold row offered.
func (a *App) cycleClassifierPhaseThreshold(phase int, opts []string) tea.Cmd {
	if len(opts) == 0 {
		return nil
	}
	cur := a.classifierPhaseThresholdValue(phase)
	next := opts[(indexOfString(opts, cur)+1)%len(opts)]
	val, err := strconv.ParseFloat(next, 64)
	if err != nil {
		return nil
	}
	return a.mutateClassifier(func(c *config.ClassifierSettings) {
		if phase == 1 {
			c.Phase1.Threshold = val
		} else {
			c.Phase2.Threshold = val
		}
	})
}

// applyModelProvider commits a provider/model/effort to the running session
// without touching settings files (the picker's "session" scope). It is the
// shared tail of the model picker and resume restore so the two cannot drift.
func (a *App) applyModelProvider(provider, model, effort string) tea.Cmd {
	a.cfg.Provider = provider
	a.cfg.Model = model
	if effort != "" {
		a.settings.Effort = effort
	}
	return a.refreshProvider()
}

func modelEfforts(catalog []models.Model, midx int) []string {
	if len(catalog) == 0 {
		return nil
	}
	midx = clampIdx(midx, len(catalog))
	return catalog[midx].Efforts
}

func clampIdx(idx, n int) int {
	if n <= 0 {
		return 0
	}
	if idx < 0 || idx >= n {
		return 0
	}
	return idx
}

func indexOfString(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return -1
}

func indexOfModel(list []models.Model, id string) int {
	for i, m := range list {
		if m.ID == id {
			return i
		}
	}
	return -1
}

// trimLastRune removes the final UTF-8 rune from s, leaving s untouched when
// it is empty. Used by the filter box's backspace.
func trimLastRune(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	return string(r[:len(r)-1])
}

// reasoningEffort reports whether an effort string means reasoning is on.
// ResolveClassifier treats both "" and "none" as off.
func reasoningEffort(effort string) bool {
	e := strings.ToLower(strings.TrimSpace(effort))
	return e != "" && e != "none"
}

// classifierProvider is the provider the classifier actually uses: its own
// when set, the main one otherwise.
func (a *App) classifierProvider() string {
	if cls := a.settings.Classifier; cls != nil && cls.Provider != "" {
		return cls.Provider
	}
	return a.cfg.Provider
}

// classifierModel is the model the classifier actually uses.
func (a *App) classifierModel() string {
	if cls := a.settings.Classifier; cls != nil && cls.Model != "" {
		return cls.Model
	}
	return a.cfg.Model
}

// classifierKind resolves the effective classifier stack kind.
func (a *App) classifierKind() string {
	return run.ClassifierKind(a.settings.Classifier)
}

// resolvedClassifierPhase returns the effective phase model config using the
// same resolution the pipeline uses (run.ResolveSecurityClassifier), never a
// parallel guess.
func (a *App) resolvedClassifierPhase(phase int) *mlclassify.ModelConfig {
	sc := a.resolvedSecurityClassifier()
	if phase == 1 {
		return sc.Phase1
	}
	return sc.Phase2
}

// resolvedSecurityClassifier returns the effective security classifier config
// for the current settings, using the same resolution the pipeline uses.
func (a *App) resolvedSecurityClassifier() run.SecurityClassifierConfig {
	return run.ResolveSecurityClassifier(a.settings.Classifier)
}

// mlPhase maps the /model row's int phase to the mlclassify.Phase the
// threshold helpers need.
func mlPhase(phase int) mlclassify.Phase {
	if phase == 2 {
		return mlclassify.Phase2
	}
	return mlclassify.Phase1
}

// classifierPhaseRow builds the status/selector row for one local phase gate.
// Embedded phase 1 is locked (the binary choice cannot be overridden); remote
// phases are selectable so the source can cycle; a phase with no usable model
// reports why it is off.
func (a *App) classifierPhaseRow(phase int) settingsRow {
	key, label := "phase1", "phase 1"
	if phase == 2 {
		key, label = "phase2", "phase 2"
	}

	mc := a.resolvedClassifierPhase(phase)
	if mc == nil {
		if phase == 1 {
			return settingsRow{key: key, label: label, kind: "text",
				value: "LLM sentinel (no HuggingFace key)", disabled: true}
		}
		if a.resolvedSecurityClassifier().Phase2Deferred {
			return settingsRow{key: key, label: label, kind: "text",
				value: "deferred to phase 3", disabled: true}
		}
		return settingsRow{key: key, label: label, kind: "choose",
			opts: a.classifierPhaseOpts(phase), value: "disabled"}
	}

	// Phase 1 is always on when embedded and is not cyclable. Phase 2 stays
	// cyclable even when its embedded model is resolved, so the jailbreak gate
	// can still be turned back off from the TUI.
	if mc.Source == mlclassify.SourceEmbedded && phase == 1 {
		return settingsRow{key: key, label: label, kind: "text", value: mc.ID, disabled: true}
	}
	if mc.Source == mlclassify.SourceEmbedded {
		return settingsRow{key: key, label: label, kind: "choose",
			opts: a.classifierPhaseOpts(phase), value: mc.ID + " (embedded)"}
	}
	return settingsRow{key: key, label: label, kind: "choose",
		opts: a.classifierPhaseOpts(phase), value: mc.ID + " via huggingface"}
}

// classifierPhaseThresholdRow builds the user-adjustable threshold row for one
// phase gate. It is hidden (disabled) when the phase has no model.
func (a *App) classifierPhaseThresholdRow(phase int) settingsRow {
	key, label := "phase1-threshold", "phase 1 threshold"
	if phase == 2 {
		key, label = "phase2-threshold", "phase 2 threshold"
	}
	mc := a.resolvedClassifierPhase(phase)
	if mc == nil {
		return settingsRow{key: key, label: label, kind: "text", value: "—", disabled: true}
	}
	return settingsRow{key: key, label: label, kind: "choose",
		opts: classifierThresholdOptions, value: fmt.Sprintf("%.2f", mc.ThresholdOr(mlPhase(phase)))}
}

// classifierPhaseThresholdValue returns the effective threshold for one phase
// formatted the same way the threshold row renders it, so cycling can find the
// current value in the options list.
func (a *App) classifierPhaseThresholdValue(phase int) string {
	mc := a.resolvedClassifierPhase(phase)
	if mc == nil {
		return ""
	}
	return fmt.Sprintf("%.2f", mc.ThresholdOr(mlPhase(phase)))
}

// classifierPhaseSource returns the effective source token for one phase, for
// the source cycle to advance from.
func (a *App) classifierPhaseSource(phase int) string {
	mc := a.resolvedClassifierPhase(phase)
	if mc == nil {
		if phase == 2 {
			return "disabled"
		}
		return ""
	}
	return string(mc.Source)
}

// classifierPhaseOpts returns the source choices a phase row cycles through.
func (a *App) classifierPhaseOpts(phase int) []string {
	if phase == 1 {
		return []string{"huggingface"}
	}
	var opts []string
	if _, ok := mlclassify.EmbeddedPhase2(); ok {
		opts = append(opts, "embedded")
	}
	if a.hfToken() != "" {
		opts = append(opts, "huggingface")
	}
	// "disabled" appears only when the gate is currently on, so it is a
	// turn-off stop rather than a state the deferred/off row can reach.
	if a.resolvedClassifierPhase(2) != nil {
		opts = append(opts, "disabled")
	}
	return opts
}

// classifierPhase3Row builds the derived phase-3 status row. Phase 3 is not
// separately editable: it is on iff the classifier provider and model rows are
// both explicitly set, and this row makes that consequence visible. When the
// jailbreak gate is deferred, phase 3 also covers JAILBREAK.
func (a *App) classifierPhase3Row() settingsRow {
	cls := a.settings.Classifier
	on := cls != nil && cls.Provider != "" && cls.Model != ""
	value := "off — set classifier provider + model to enable"
	if on {
		scope := "extraction only"
		if a.resolvedSecurityClassifier().Phase2Deferred {
			scope = "jailbreak + extraction"
		}
		value = scope + " · " + a.providerDisplayLabel(cls.Provider) + "/" + cls.Model
	}
	return settingsRow{key: "phase3", label: "phase 3", kind: "text", value: value, disabled: true}
}

// classifierProviders are the providers the classifier page may offer: custom
// profiles, the built-in local servers, openrouter when a typesafe/jev model
// is available, and huggingface when a token is configured. Other built-ins
// (openai, anthropic, …) are general-chat providers and are not
// classifier-capable, so they never appear for this role.
func (a *App) classifierProviders() []string {
	allowed := func(name string) bool {
		switch name {
		case "huggingface":
			return a.hfToken() != ""
		case "openrouter":
			return a.classifierOpenRouterAvailable()
		case "llama-server", "ollama":
			return true
		default:
			return !provider.Builtin(name) // custom profiles only
		}
	}
	var out []string
	for _, name := range a.providerNames() {
		if allowed(name) {
			out = append(out, name)
		}
	}
	return out
}

// classifierOpenRouterAvailable reports whether openrouter is offered to the
// classifier role. It needs only that the provider is configured: the model
// picker filters to typesafe/jev* when it is selected. Requiring the Jev model
// to already be in the (not-yet-fetched) catalogue would make the provider
// unreachable — the live fetch that surfaces typesafe/jev only happens after
// openrouter is chosen.
func (a *App) classifierOpenRouterAvailable() bool {
	return a.providerConfigured("openrouter")
}

// providerConfigured reports whether a provider's credentials resolve through
// the resolver, falling back to the environment source when no resolver is
// set (the non-interactive construction path and most tests).
func (a *App) providerConfigured(name string) bool {
	if a.resolver != nil {
		return a.resolver.Configured(name)
	}
	_, status := run.Prepare("", name, run.CredentialSource(run.EnvSource(os.Getenv)))
	return status.Configured
}

// classifierEffortOpts returns the effort chips for the classifier's model.
func (a *App) classifierEffortOpts() []string {
	catalog := a.catalogFor(a.classifierProvider())
	if efforts := modelEfforts(catalog, indexOfModel(catalog, a.classifierModel())); len(efforts) > 0 {
		return efforts
	}
	return defaultModelEfforts
}

// agentEffortOpts returns the effort chips for the agent's model.
func (a *App) agentEffortOpts() []string {
	catalog := a.catalogFor(a.cfg.Provider)
	if efforts := modelEfforts(catalog, indexOfModel(catalog, a.cfg.Model)); len(efforts) > 0 {
		return efforts
	}
	return defaultModelEfforts
}

func (a *App) openAgentModelPicker() tea.Cmd {
	a.modelState.picking = true
	a.modelState.pickingRole = roleAgent
	name := a.cfg.Provider
	if name == "" {
		if providers := a.modelProviders(); len(providers) > 0 {
			name = providers[0]
		}
	}
	a.modelState.filter = ""
	a.modelState.filtering = false
	a.modelState.scroll = 0
	a.modelState.modelIdx = indexOfModel(a.catalogFor(name), a.cfg.Model)
	return a.fetchCatalogCmd(name)
}

func (a *App) openClassifierModelPicker() tea.Cmd {
	name := a.classifierProvider()
	a.modelState.picking = true
	a.modelState.pickingRole = roleClassifier
	a.modelState.filter = ""
	a.modelState.filtering = false
	a.modelState.scroll = 0
	a.modelState.modelIdx = indexOfModel(a.catalogFor(name), a.classifierModel())
	return a.fetchCatalogCmd(name)
}

// cycleAgentProvider moves the agent's provider to the next one in the
// picker's list, wrapping from the last back to the first. The list is
// exactly what the picker offers (availableProviders, which pins the
// committed provider), so the cycle reaches every authenticated provider in
// canonical order and the cursor is never off the ring.
//
// There is deliberately no "" (unset) stop in the agent ring: every commit
// ends in refreshProvider, and run.Prepare normalises an empty provider to
// the default (openai). An "" stop therefore bounces on the very next wrap,
// so the cycle only ever traversed the tail of the sorted list starting at
// the default and providers sorting before the committed one were
// unreachable. Unsetting is the x key's job.
func (a *App) cycleAgentProvider(opts []string) tea.Cmd {
	if len(opts) == 0 {
		return nil
	}
	cur := a.cfg.Provider
	next := opts[(indexOfString(opts, cur)+1)%len(opts)]
	return a.mutateAgent(func(s *config.Settings) {
		s.Provider = next
		s.Model = ""
	}, func() {
		a.cfg.Provider = next
		a.cfg.Model = ""
	})
}

func (a *App) cycleAgentEffort(opts []string) tea.Cmd {
	if len(opts) == 0 {
		return nil
	}
	cur := a.cfg.Effort
	next := opts[(indexOfString(opts, cur)+1)%len(opts)]
	return a.mutateAgent(func(s *config.Settings) { s.Effort = next }, func() {
		a.cfg.Effort = next
		a.settings.Effort = next
	})
}

func (a *App) cycleClassifierProvider(opts []string) tea.Cmd {
	cur := ""
	if cls := a.settings.Classifier; cls != nil {
		cur = cls.Provider
	}
	ring := append([]string{""}, opts...)
	next := ring[(indexOfString(ring, cur)+1)%len(ring)]
	return a.mutateClassifier(func(c *config.ClassifierSettings) {
		c.Provider = next
		c.Model = ""
	})
}

func (a *App) cycleClassifierEffort(opts []string) tea.Cmd {
	if len(opts) == 0 {
		return nil
	}
	cur := ""
	if cls := a.settings.Classifier; cls != nil {
		cur = cls.Effort
	}
	next := opts[(indexOfString(opts, cur)+1)%len(opts)]
	a.modelState.classifierLastEffort = next
	return a.mutateClassifier(func(c *config.ClassifierSettings) { c.Effort = next })
}

// toggleClassifierReasoning drives the effort field: off stores "none", on
// restores the remembered chip.
func (a *App) toggleClassifierReasoning() tea.Cmd {
	cls := a.settings.Classifier
	if cls != nil && reasoningEffort(cls.Effort) {
		a.modelState.classifierLastEffort = cls.Effort
		return a.mutateClassifier(func(c *config.ClassifierSettings) { c.Effort = "none" })
	}
	restore := a.modelState.classifierLastEffort
	if !reasoningEffort(restore) {
		opts := a.classifierEffortOpts()
		restore = opts[0]
	}
	return a.mutateClassifier(func(c *config.ClassifierSettings) { c.Effort = restore })
}

// toggleAgentReasoning drives the main model's reasoning: off stores "none"
// (the single value that suppresses the reasoning hint), on restores the
// remembered effort, defaulting to the provider default ("").
func (a *App) toggleAgentReasoning() tea.Cmd {
	if a.settings.Effort != "none" {
		a.modelState.agentLastEffort = a.settings.Effort
		return a.mutateAgent(func(s *config.Settings) { s.Effort = "none" }, func() {
			a.cfg.Effort = "none"
			a.settings.Effort = "none"
		})
	}
	restore := a.modelState.agentLastEffort
	return a.mutateAgent(func(s *config.Settings) { s.Effort = restore }, func() {
		a.cfg.Effort = restore
		a.settings.Effort = restore
	})
}

// clearCaveman resets the caveman project preference, falling back to the
// settings-file value.
func (a *App) clearCaveman() tea.Cmd {
	if err := a.persistPref(func(p *config.ProjectPrefs) { p.Caveman = nil }); err != nil {
		a.modelState.errorMsg = err.Error()
		return nil
	}
	a.invalidateAgentSession()
	return nil
}

// clearGuardrails resets the guardrails project preference and override.
func (a *App) clearGuardrails() tea.Cmd {
	a.guardrailsOverride = nil
	if err := a.persistPref(func(p *config.ProjectPrefs) { p.Guardrails = nil }); err != nil {
		a.modelState.errorMsg = err.Error()
		return nil
	}
	a.invalidateAgentSession()
	a.syncPosture()
	return nil
}

// clearAsk resets the ask-permission project preference and override.
func (a *App) clearAsk() tea.Cmd {
	a.askOverride = nil
	if err := a.persistPref(func(p *config.ProjectPrefs) { p.AskPermission = nil }); err != nil {
		a.modelState.errorMsg = err.Error()
		return nil
	}
	a.invalidateAgentSession()
	a.syncPosture()
	return nil
}

// clearFirewall resets the firewall project preference and override, then
// re-resolves the gateway routing so the session stops routing through it.
func (a *App) clearFirewall() tea.Cmd {
	a.firewallOverride = nil
	if err := a.persistPref(func(p *config.ProjectPrefs) { p.FirewallEnabled = nil }); err != nil {
		a.modelState.errorMsg = err.Error()
		return nil
	}
	if a.resolver != nil {
		if cfg, err := a.resolveConfig(); err == nil {
			a.cfg = cfg
		}
	}
	a.refreshFooter()
	return nil
}

// unsetClassifierRow clears one classifier field. Clearing provider/model drops
// the block.
func (a *App) unsetClassifierRow(key string) tea.Cmd {
	return a.mutateClassifier(func(c *config.ClassifierSettings) {
		switch key {
		case "kind":
			c.Kind = ""
		case "phase1":
			c.Phase1 = config.ClassifierPhaseSettings{}
		case "phase1-threshold":
			c.Phase1.Threshold = 0
		case "phase2":
			c.Phase2 = config.ClassifierPhaseSettings{}
		case "phase2-threshold":
			c.Phase2.Threshold = 0
		case "provider":
			c.Provider = ""
			c.Model = ""
		case "model":
			c.Model = ""
		case "reasoning", "effort":
			c.Effort = ""
		}
	})
}

// mutateAgent applies a settings change according to the agent role scope.
// fn mutates the settings structure (written for global/project scope);
// sessionFn mirrors the change onto the running config. Every path ends in
// refreshProvider: a provider or model edit must re-resolve the base URL and
// API key, otherwise the session keeps sending to the previous provider's
// endpoint (an openrouter edit after a gateway provider kept routing to the
// gateway, and a llama-server edit kept routing its model id to the gateway).
func (a *App) mutateAgent(fn func(*config.Settings), sessionFn func()) tea.Cmd {
	scope := a.modelState.agentScope
	if scope == "" {
		scope = "session"
	}
	if scope == "session" {
		if sessionFn != nil {
			sessionFn()
		}
		a.state.Provider = a.cfg.Provider
		a.state.Model = a.cfg.Model
		a.state.Effort = a.settings.Effort
		a.state.LastMode = a.mode
		_ = config.SaveState(a.state)
		a.modelState.errorMsg = ""
		return a.refreshProvider()
	}
	cfgScope := config.ScopeProject
	if scope == "global" {
		cfgScope = config.ScopeGlobal
	}
	if err := config.Mutate(cfgScope, a.workdir, func(s *config.Settings) error {
		fn(s)
		return nil
	}); err != nil {
		a.modelState.errorMsg = err.Error()
		return nil
	}
	if err := a.reloadSettings(); err != nil {
		a.modelState.errorMsg = err.Error()
		return nil
	}
	// reloadSettings refreshes a.settings but not a.cfg; mirror the persisted
	// change onto the running config before re-resolving the wire settings.
	if sessionFn != nil {
		sessionFn()
	}
	a.modelState.errorMsg = ""
	return a.refreshProvider()
}

// mutateClassifier applies fn to the classifier block in the page's scope,
// reloads the merged settings, and reinstalls the live classifier.
func (a *App) mutateClassifier(fn func(*config.ClassifierSettings)) tea.Cmd {
	scope := a.modelState.classifierScope
	if scope == "" {
		scope = "project"
	}
	cfgScope := config.ScopeProject
	if scope == "global" {
		cfgScope = config.ScopeGlobal
	}
	if err := config.Mutate(cfgScope, a.workdir, func(s *config.Settings) error {
		if s.Classifier == nil {
			s.Classifier = &config.ClassifierSettings{}
		}
		fn(s.Classifier)
		return nil
	}); err != nil {
		a.modelState.errorMsg = err.Error()
		return nil
	}
	if err := a.reloadSettings(); err != nil {
		a.modelState.errorMsg = err.Error()
		return nil
	}
	return a.applyClassifierChange()
}

// applyClassifierChange re-resolves the classifier and reports a resolve
// failure on the page instead of silently falling back.
func (a *App) applyClassifierChange() tea.Cmd {
	src := run.CredentialSource(run.EnvSource(os.Getenv))
	if a.resolver != nil {
		src = a.resolver
	}
	if _, err := run.ResolveClassifier(a.cfg, a.settings.Classifier, src); err != nil {
		a.modelState.errorMsg = err.Error()
		return nil
	}
	a.modelState.errorMsg = ""
	return a.refreshProvider()
}

func humanizeBytes(n int) string {
	switch {
	case n >= 1024*1024:
		return fmt.Sprintf("%d MiB", n/(1024*1024))
	case n >= 1024:
		return fmt.Sprintf("%d KiB", n/1024)
	default:
		return fmt.Sprintf("%d B", n)
	}
}
