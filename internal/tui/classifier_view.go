package tui

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/models"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/tui/components"
)

// classifierViewState tracks the /classifier page: the role manager's own
// model, chosen independently of the agent's.
type classifierViewState struct {
	selected int
	scope    config.Scope
	errorMsg string

	// lastEffort remembers the chip to restore when reasoning is toggled back
	// on, so off/on is not a silent downgrade to the head of the list.
	lastEffort string

	// model sub-picker, entered from the model row.
	picking   bool
	modelIdx  int
	filter    string
	filtering bool
	scroll    int
}

// defaultClassifierEfforts is the effort set offered when the selected model
// advertises none — a custom provider's profile carries no catalogue.
var defaultClassifierEfforts = []string{"low", "medium", "high"}

// enterClassifier seeds the page from the effective settings and refreshes the
// catalogue and availability answer the rows are rendered from.
func (a *App) enterClassifier() tea.Cmd {
	scope := a.classifierState.scope
	if scope == "" {
		scope = config.ScopeProject
	}
	last := a.classifierState.lastEffort
	if cls := a.settings.Classifier; cls != nil && reasoningEffort(cls.Effort) {
		last = cls.Effort
	}
	if last == "" {
		last = "medium"
	}
	a.classifierState = classifierViewState{scope: scope, lastEffort: last}
	return tea.Batch(a.fetchCatalogCmd(a.classifierProvider()), a.availabilityCmdIfStale())
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

// classifierProviders are the providers the page may offer: the available
// ones, plus whatever the agent and the classifier are already committed to.
func (a *App) classifierProviders() []string {
	var pinned string
	if cls := a.settings.Classifier; cls != nil {
		pinned = cls.Provider
	}
	return a.availableProviders(a.cfg.Provider, pinned)
}

// classifierEffortOpts returns the effort chips for the classifier's model.
func (a *App) classifierEffortOpts() []string {
	catalog := a.catalogFor(a.classifierProvider())
	if efforts := modelEfforts(catalog, indexOfModel(catalog, a.classifierModel())); len(efforts) > 0 {
		return efforts
	}
	return defaultClassifierEfforts
}

// classifierRows builds the declarative row table, mirroring settingsRows.
func (a *App) classifierRows() []settingsRow {
	cls := a.settings.Classifier
	src := sourceLabel(a.eff.Origin["classifier"])

	providerVal := fmt.Sprintf("— (main: %s)", a.cfg.Provider)
	if cls != nil && cls.Provider != "" {
		providerVal = cls.Provider
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
	cavemanVal := boolLabel(a.settings.ClassifierCavemanEnabled()) + "  (prose payloads only)"

	return []settingsRow{
		{key: "provider", label: "provider", kind: "choose", opts: a.classifierProviders(), value: providerVal, src: src},
		{key: "model", label: "model", kind: "pick", value: modelVal, src: src},
		{key: "reasoning", label: "reasoning", kind: "toggle", value: boolLabel(on), src: src},
		{key: "effort", label: "effort", kind: "choose", opts: a.classifierEffortOpts(), value: effortVal, src: src, disabled: !on},
		{key: "caveman", label: "caveman", kind: "toggle", value: cavemanVal, src: src},
	}
}

func (a *App) classifierView() string {
	w := a.contentWidth()
	var b strings.Builder
	b.WriteString(components.SectionHeader("Classifier Model", "esc back", w))
	b.WriteString(components.WarnStyle.Render(
		"! the classifier is the security gate for tool output; a weaker model means weaker detection") + "\n\n")

	path := config.ProjectSettingsPath(a.workdir)
	if a.classifierState.scope == config.ScopeGlobal {
		p, _ := config.GlobalSettingsPath()
		path = p
	}
	scope := string(a.classifierState.scope)
	if scope == "" {
		scope = string(config.ScopeProject)
	}
	b.WriteString(components.Chip(scope, components.ColorTealSoft) +
		"  " + components.MutedStyle.Render(path) + "\n\n")

	if a.classifierState.picking {
		b.WriteString(a.classifierPicker())
		return lipgloss.NewStyle().Padding(1).Render(b.String())
	}

	for i, row := range a.classifierRows() {
		selected := i == a.classifierState.selected
		label := fmt.Sprintf("%-20s", row.label)
		value := fmt.Sprintf("%-37s ", row.value)
		switch {
		case row.disabled:
			label = components.MutedStyle.Render(label)
			value = components.MutedStyle.Render(value)
		case selected:
			label = components.AccentStyle.Bold(true).Render(label)
			value = components.EmphStyle.Render(value)
		default:
			label = components.MutedStyle.Render(label)
		}
		b.WriteString(components.Cursor(selected) + label + value +
			components.MutedStyle.Render(row.src) + "\n")
	}

	if a.avail.note != "" {
		b.WriteString("\n" + components.MutedStyle.Render(a.avail.note) + "\n")
	}
	if a.classifierState.errorMsg != "" {
		b.WriteString("\n" + components.DangerStyle.Render("✗ "+a.classifierState.errorMsg) + "\n")
	}
	b.WriteString("\n" + components.HelpBar(
		"↑↓", "move", "space", "change", "x", "unset", "s", "scope", "esc", "back") + "\n")
	return lipgloss.NewStyle().Padding(1).Render(b.String())
}

// classifierPicker renders the model sub-picker over the classifier's own
// provider catalogue.
func (a *App) classifierPicker() string {
	name, catalog := a.classifierCatalog()
	var b strings.Builder
	b.WriteString(components.MutedStyle.Render("model for ") + components.Chip(name, components.ColorTeal) + "\n")

	switch {
	case a.classifierState.filtering:
		b.WriteString(components.AccentStyle.Render("search  "+a.classifierState.filter+"▌") + "\n")
	case a.classifierState.filter != "":
		b.WriteString(components.MutedStyle.Render("search  ") +
			components.EmphStyle.Render(a.classifierState.filter) + "\n")
	default:
		b.WriteString(components.MutedStyle.Render("search  / to filter") + "\n")
	}

	const rows = 10
	if len(catalog) == 0 {
		b.WriteString("\n" + components.MutedStyle.Render("  no catalogue for this provider — esc to cancel") + "\n")
	} else {
		midx := clampIdx(a.classifierState.modelIdx, len(catalog))
		start := windowStart(a.classifierState.scroll, midx, len(catalog), rows)
		a.classifierState.scroll = start
		end := min(start+rows, len(catalog))
		for i := start; i < end; i++ {
			selected := i == midx
			line := fmt.Sprintf("%-40s", catalog[i].ID)
			if selected {
				line = components.EmphStyle.Render(line)
			} else {
				line = components.MutedStyle.Render(line)
			}
			b.WriteString(components.Cursor(selected) + line + "\n")
		}
		b.WriteString(components.MutedStyle.Render(fmt.Sprintf("  %d/%d", midx+1, len(catalog))) + "\n")
	}

	if a.classifierState.filtering {
		b.WriteString("\n" + components.HelpBar("type", "filter", "enter", "accept", "esc", "clear") + "\n")
	} else {
		b.WriteString("\n" + components.HelpBar("↑↓", "model", "/", "filter", "enter", "set", "esc", "cancel") + "\n")
	}
	return b.String()
}

// classifierCatalog resolves the classifier provider's filtered catalogue.
// Render, cursor movement and commit all read it, so the index always refers
// to the same slice.
func (a *App) classifierCatalog() (string, []models.Model) {
	name := a.classifierProvider()
	return name, filterModels(a.catalogFor(name), a.classifierState.filter)
}

func (a *App) handleClassifierKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.classifierState.picking {
		return a.handleClassifierPickerKey(m)
	}
	rows := a.classifierRows()
	switch m.String() {
	case "esc":
		a.pop()
		return a, nil
	case "up", "k":
		if a.classifierState.selected > 0 {
			a.classifierState.selected--
		}
		return a, nil
	case "down", "j":
		if a.classifierState.selected < len(rows)-1 {
			a.classifierState.selected++
		}
		return a, nil
	case "s":
		if a.classifierState.scope == config.ScopeGlobal {
			a.classifierState.scope = config.ScopeProject
		} else {
			a.classifierState.scope = config.ScopeGlobal
		}
		return a, nil
	case "x":
		if a.classifierState.selected >= len(rows) {
			return a, nil
		}
		return a, a.unsetClassifierRow(rows[a.classifierState.selected].key)
	case " ", "enter":
		if a.classifierState.selected >= len(rows) {
			return a, nil
		}
		row := rows[a.classifierState.selected]
		if row.disabled {
			return a, nil
		}
		return a, a.changeClassifierRow(row)
	}
	return a, nil
}

func (a *App) handleClassifierPickerKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	// The filter box matches by message type, like handleModelFilterKey: the
	// vim keys must stay typeable inside the box instead of moving the cursor.
	if a.classifierState.filtering {
		switch m.Type {
		case tea.KeyEsc:
			a.classifierState.filtering = false
			a.classifierState.filter = ""
			a.classifierState.modelIdx = 0
			a.classifierState.scroll = 0
		case tea.KeyEnter:
			a.classifierState.filtering = false
		case tea.KeyRunes:
			a.classifierState.filter += string(m.Runes)
			a.classifierState.modelIdx = 0
			a.classifierState.scroll = 0
		case tea.KeySpace:
			a.classifierState.filter += " "
			a.classifierState.modelIdx = 0
			a.classifierState.scroll = 0
		case tea.KeyBackspace:
			a.classifierState.filter = trimLastRune(a.classifierState.filter)
			a.classifierState.modelIdx = 0
			a.classifierState.scroll = 0
		}
		return a, nil
	}

	_, catalog := a.classifierCatalog()
	switch m.String() {
	case "esc":
		a.classifierState.picking = false
		a.classifierState.filter = ""
		return a, nil
	case "/":
		a.classifierState.filtering = true
		return a, nil
	case "up", "k":
		if a.classifierState.modelIdx > 0 {
			a.classifierState.modelIdx--
		} else if len(catalog) > 0 {
			a.classifierState.modelIdx = len(catalog) - 1
		}
		return a, nil
	case "down", "j":
		if len(catalog) > 0 && a.classifierState.modelIdx < len(catalog)-1 {
			a.classifierState.modelIdx++
		} else {
			a.classifierState.modelIdx = 0
		}
		return a, nil
	case " ", "enter":
		if len(catalog) == 0 {
			a.classifierState.picking = false
			return a, nil
		}
		id := catalog[clampIdx(a.classifierState.modelIdx, len(catalog))].ID
		a.classifierState.picking = false
		a.classifierState.filter = ""
		return a, a.mutateClassifier(func(c *config.ClassifierSettings) { c.Model = id })
	}
	return a, nil
}

// changeClassifierRow applies the row's own change: cycle for a choice, flip
// for a toggle, open the sub-picker for the model.
func (a *App) changeClassifierRow(row settingsRow) tea.Cmd {
	switch row.key {
	case "provider":
		return a.cycleClassifierProvider(row.opts)
	case "model":
		name, catalog := a.classifierCatalog()
		a.classifierState.picking = true
		a.classifierState.modelIdx = clampIdx(indexOfModel(catalog, a.classifierModel()), len(catalog))
		a.classifierState.scroll = 0
		return a.fetchCatalogCmd(name)
	case "reasoning":
		return a.toggleClassifierReasoning()
	case "effort":
		return a.cycleClassifierEffort(row.opts)
	case "caveman":
		on := !a.settings.ClassifierCavemanEnabled()
		return a.mutateClassifier(func(c *config.ClassifierSettings) { c.Caveman = &on })
	}
	return nil
}

// cycleClassifierProvider steps through the offered providers, with an unset
// stop so the classifier can be handed back to the main provider. Changing
// provider clears the model: a model id is only meaningful to its own
// provider, and carrying one across would resolve to nothing.
func (a *App) cycleClassifierProvider(opts []string) tea.Cmd {
	cur := ""
	if cls := a.settings.Classifier; cls != nil {
		cur = cls.Provider
	}
	// The cycle is "unset" followed by every offered provider.
	ring := append([]string{""}, opts...)
	next := ring[(indexOfString(ring, cur)+1)%len(ring)]
	return a.mutateClassifier(func(c *config.ClassifierSettings) {
		c.Provider = next
		c.Model = ""
	})
}

// toggleClassifierReasoning drives the effort field: off stores the literal
// "none", on restores the remembered chip. There is no separate reasoning key
// to fall out of step with the effort.
func (a *App) toggleClassifierReasoning() tea.Cmd {
	cls := a.settings.Classifier
	if cls != nil && reasoningEffort(cls.Effort) {
		a.classifierState.lastEffort = cls.Effort
		return a.mutateClassifier(func(c *config.ClassifierSettings) { c.Effort = "none" })
	}
	restore := a.classifierState.lastEffort
	if !reasoningEffort(restore) {
		opts := a.classifierEffortOpts()
		restore = opts[0]
	}
	return a.mutateClassifier(func(c *config.ClassifierSettings) { c.Effort = restore })
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
	a.classifierState.lastEffort = next
	return a.mutateClassifier(func(c *config.ClassifierSettings) { c.Effort = next })
}

// unsetClassifierRow clears one field. Clearing them all drops the classifier
// block from the file, and the classifier falls back to the main model — the
// documented default.
func (a *App) unsetClassifierRow(key string) tea.Cmd {
	return a.mutateClassifier(func(c *config.ClassifierSettings) {
		switch key {
		case "provider":
			c.Provider = ""
			c.Model = ""
		case "model":
			c.Model = ""
		case "reasoning", "effort":
			c.Effort = ""
		case "caveman":
			c.Caveman = nil
		}
	})
}

// mutateClassifier applies fn to the classifier block in the page's scope,
// reloads the merged settings, and reinstalls the live classifier so the
// change takes effect on the next turn rather than the next launch.
func (a *App) mutateClassifier(fn func(*config.ClassifierSettings)) tea.Cmd {
	scope := a.classifierState.scope
	if scope == "" {
		scope = config.ScopeProject
	}
	if err := config.Mutate(scope, a.workdir, func(s *config.Settings) error {
		if s.Classifier == nil {
			s.Classifier = &config.ClassifierSettings{}
		}
		fn(s.Classifier)
		return nil
	}); err != nil {
		a.classifierState.errorMsg = err.Error()
		return nil
	}
	if err := a.reloadSettings(); err != nil {
		a.classifierState.errorMsg = err.Error()
		return nil
	}
	return a.applyClassifierChange()
}

// applyClassifierChange re-resolves the classifier and reports a resolve
// failure on the page. refreshProvider swallows that error and falls back to
// the main model, which is the wrong kind of quiet here: the user just chose
// which model guards their tool output and has to know it did not take.
func (a *App) applyClassifierChange() tea.Cmd {
	src := run.CredentialSource(run.EnvSource(os.Getenv))
	if a.resolver != nil {
		src = a.resolver
	}
	if _, err := run.ResolveClassifier(a.cfg, a.settings.Classifier, src); err != nil {
		a.classifierState.errorMsg = err.Error()
		return nil
	}
	a.classifierState.errorMsg = ""
	return a.refreshProvider()
}
