package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/modelfetch"
	"github.com/vulnetix/signet/internal/models"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/tui/components"
)

// modelViewState tracks the /model picker UI.
type modelViewState struct {
	providerIdx int
	modelIdx    int // index into the FILTERED catalogue
	effortIdx   int
	scope       string // session | global | project
	errorMsg    string

	filtering bool   // search box has focus
	filter    string // active substring filter
	scroll    int    // first visible row of the filtered catalogue
}

func (a *App) enterModel() tea.Cmd {
	providers := a.providerNames()
	pidx := indexOfString(providers, a.cfg.Provider)
	if pidx < 0 {
		pidx = 0
	}
	catalog := a.catalogFor(providers[pidx])
	midx := indexOfModel(catalog, a.cfg.Model)
	if midx < 0 {
		midx = 0
	}
	efforts := modelEfforts(catalog, midx)
	eidx := indexOfString(efforts, a.settings.Effort)
	if eidx < 0 {
		eidx = indexOfString(efforts, "medium")
	}
	if eidx < 0 {
		eidx = 0
	}
	a.modelState = modelViewState{providerIdx: pidx, modelIdx: midx, effortIdx: eidx, scope: "project"}
	return a.fetchCatalogCmd(providers[pidx])
}

// modelsFetchedMsg carries the result of an async live-catalogue fetch.
type modelsFetchedMsg struct {
	provider string
	models   []models.Model
	err      error
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
	if _, ok := a.catalogCache[name]; ok || a.catalogLoading[name] {
		return nil
	}
	a.catalogLoading[name] = true
	client := a.client
	resolver := a.resolver
	return func() tea.Msg {
		src := run.CredentialSource(run.EnvSource(os.Getenv))
		if resolver != nil {
			src = resolver
		}
		// Resolve the target provider (not the currently-committed one) so
		// browsing previewed providers still fetches from the right endpoint.
		cfg, _ := run.Prepare("", name, src)
		target := modelfetch.Target{Name: name, BaseURL: cfg.BaseURL, APIKey: cfg.APIKey, Auth: cfg.Auth, API: cfg.API}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		m, err := modelfetch.List(ctx, target, client)
		return modelsFetchedMsg{provider: name, models: m, err: err}
	}
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
	if m.err != nil {
		a.catalogErr[m.provider] = m.err.Error()
		return nil
	}
	delete(a.catalogErr, m.provider)
	a.catalogCache[m.provider] = m.models
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
func (a *App) modelCatalog() (string, []models.Model) {
	providers := a.providerNames()
	pidx := clampIdx(a.modelState.providerIdx, len(providers))
	name := providers[pidx]
	return name, filterModels(a.catalogFor(name), a.modelState.filter)
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

// modelHelpBar returns the picker's key bar, swapping to the filter box's own
// keys while the box has focus.
func (a *App) modelHelpBar() string {
	if a.modelState.filtering {
		return components.HelpBar("type", "filter", "↑↓", "model", "enter", "accept", "esc", "clear")
	}
	return components.HelpBar(
		"←→", "provider", "↑↓", "model", "/", "filter",
		"e", "effort", "s", "scope", "c", "credentials",
		"enter", "set", "esc", "cancel")
}

func (a *App) modelView() string {
	providers := a.providerNames()
	pidx := clampIdx(a.modelState.providerIdx, len(providers))
	name, catalog := a.modelCatalog()
	midx := clampIdx(a.modelState.modelIdx, len(catalog))
	efforts := modelEfforts(catalog, midx)
	eidx := clampIdx(a.modelState.effortIdx, len(efforts))

	w := a.contentWidth()

	// Pre-list chrome: section header, provider tabs, search line.
	var head strings.Builder
	head.WriteString(components.SectionHeader("Model & Provider", "esc cancel", w))

	var tabs []string
	for i, name := range providers {
		if i == pidx {
			tabs = append(tabs, components.Chip(name, components.ColorTeal))
			continue
		}
		tabs = append(tabs, components.MutedStyle.Render(" "+name+" "))
	}
	head.WriteString(strings.Join(tabs, " ") + "\n\n")
	head.WriteString(a.modelSearchLine() + "\n")

	// Post-list chrome: effort, scope, error and the help bar. The counter/
	// overflow line is rendered below the list as metaLine, outside this chunk.
	var tail strings.Builder
	tail.WriteString("\n" + components.MutedStyle.Render("effort  "))
	if len(efforts) == 0 {
		tail.WriteString(components.MutedStyle.Render("unavailable (custom provider)") + "\n")
	} else {
		var chips []string
		for i, e := range efforts {
			if i == eidx {
				chips = append(chips, components.Chip(e, components.ColorTealSoft))
				continue
			}
			chips = append(chips, components.MutedStyle.Render(" "+e+" "))
		}
		tail.WriteString(strings.Join(chips, " ") + "\n")
	}
	tail.WriteString(components.MutedStyle.Render("scope   ") +
		components.Chip(a.modelState.scope, components.ColorAmber) + "\n")

	if a.modelState.errorMsg != "" {
		tail.WriteString("\n" + components.DangerStyle.Render("✗ "+a.modelState.errorMsg) + "\n")
	}
	if errMsg := a.catalogErr[name]; errMsg != "" {
		tail.WriteString("\n" + components.DangerStyle.Render("✗ fetch: "+errMsg) + "\n")
	}
	tail.WriteString("\n" + a.modelHelpBar() + "\n")

	// Available list rows are measured, not guessed, so chrome never scrolls
	// off screen. Without a WindowSizeMsg yet (height 0) fall back to 10 rows,
	// mirroring contentWidth's narrow-terminal guard.
	const fallbackRows = 10
	rows := fallbackRows
	if a.height > 0 {
		rows = a.height - lipgloss.Height(head.String()) - lipgloss.Height(tail.String()) - 2 // Padding(1) top+bottom
	}
	if rows < 3 {
		rows = 3
	}

	// The windowed list, plus the counter/overflow affordance under it.
	var body strings.Builder
	metaLine := ""
	switch {
	case a.catalogLoading[name]:
		body.WriteString(components.AccentStyle.Render("  ○ Fetching models…") + "\n")
	case len(catalog) == 0:
		body.WriteString(components.MutedStyle.Render("  no models in this profile — type or import a model id") + "\n")
	default:
		a.modelState.scroll = windowStart(a.modelState.scroll, midx, len(catalog), rows)
		start := a.modelState.scroll
		end := start + rows
		if end > len(catalog) {
			end = len(catalog)
		}
		for i := start; i < end; i++ {
			m := catalog[i]
			selected := i == midx
			id := m.ID
			if selected {
				id = components.EmphStyle.Render(id)
			}
			line := components.Cursor(selected) + id
			if m.ID == a.cfg.Model && name == a.cfg.Provider {
				line += components.AccentStyle.Render("  ● current")
			}
			body.WriteString(line + "\n")
		}

		var meta []string
		if a.modelState.scroll > 0 {
			meta = append(meta, fmt.Sprintf("↑ %d more", a.modelState.scroll))
		}
		if end < len(catalog) {
			meta = append(meta, fmt.Sprintf("↓ %d more", len(catalog)-end))
		}
		counter := fmt.Sprintf("%d/%d", midx+1, len(catalog))
		if a.modelState.filter != "" {
			counter += fmt.Sprintf(" (of %d)", len(a.catalogFor(name)))
		}
		meta = append(meta, counter)
		metaLine = components.MutedStyle.Render("  "+strings.Join(meta, "  ·  ")) + "\n"
	}

	return lipgloss.NewStyle().Padding(1).Render(head.String() + body.String() + metaLine + tail.String())
}

func (a *App) handleModelKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.modelState.filtering {
		return a.handleModelFilterKey(m)
	}
	switch m.String() {
	case "esc":
		if a.modelState.filter != "" {
			a.modelState.filter = ""
			a.modelState.modelIdx = 0
			a.modelState.scroll = 0
			return a, nil
		}
		a.pop()
		return a, nil
	case "left", "h":
		a.modelState.providerIdx = (a.modelState.providerIdx - 1 + len(a.providerNames())) % len(a.providerNames())
		a.modelState.modelIdx = 0
		a.modelState.effortIdx = 0
		a.modelState.scroll = 0
		a.modelState.filter = ""
		return a, a.fetchCatalogCmd(a.providerNames()[a.modelState.providerIdx])
	case "right", "l":
		a.modelState.providerIdx = (a.modelState.providerIdx + 1) % len(a.providerNames())
		a.modelState.modelIdx = 0
		a.modelState.effortIdx = 0
		a.modelState.scroll = 0
		a.modelState.filter = ""
		return a, a.fetchCatalogCmd(a.providerNames()[a.modelState.providerIdx])
	case "/":
		a.modelState.filtering = true
		return a, nil
	case "up", "k":
		_, cat := a.modelCatalog()
		if a.modelState.modelIdx > 0 {
			a.modelState.modelIdx--
		} else if len(cat) > 0 {
			a.modelState.modelIdx = len(cat) - 1
		}
		a.modelState.effortIdx = 0
		return a, nil
	case "down", "j":
		_, cat := a.modelCatalog()
		if len(cat) > 0 && a.modelState.modelIdx < len(cat)-1 {
			a.modelState.modelIdx++
		} else {
			a.modelState.modelIdx = 0
		}
		a.modelState.effortIdx = 0
		return a, nil
	case "e":
		_, cat := a.modelCatalog()
		efforts := modelEfforts(cat, a.modelState.modelIdx)
		if len(efforts) == 0 {
			return a, nil
		}
		a.modelState.effortIdx = (a.modelState.effortIdx + 1) % len(efforts)
		return a, nil
	case "s":
		switch a.modelState.scope {
		case "session":
			a.modelState.scope = "global"
		case "global":
			a.modelState.scope = "project"
		case "project":
			a.modelState.scope = "session"
		}
		return a, nil
	case "r":
		name, _ := a.modelCatalog()
		delete(a.catalogCache, name)
		delete(a.catalogErr, name)
		return a, a.fetchCatalogCmd(name)
	case "c":
		if a.modelState.providerIdx < len(a.credentialState.providers) {
			a.credentialState.selectedIdx = a.modelState.providerIdx
		}
		return a, a.push(viewCredentials)
	case "enter":
		return a, a.commitModel()
	}

	cmd := a.editor.Update(m)
	return a, cmd
}

// handleModelFilterKey runs the search box's sub-mode. Named keys leave or
// steer the box; runes, space and backspace accumulate into the filter the way
// handleHistoryKey does — without the shared editor, which is a 3-line
// textarea and would eat three rows. Runes are matched by message type so the
// vim keys stay typeable inside the box instead of moving the cursor.
func (a *App) handleModelFilterKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.Type {
	case tea.KeyEsc:
		a.modelState.filtering = false
		a.modelState.filter = ""
		a.modelState.modelIdx = 0
		a.modelState.scroll = 0
		return a, nil
	case tea.KeyEnter:
		a.modelState.filtering = false
		return a, nil
	case tea.KeyUp:
		_, cat := a.modelCatalog()
		if a.modelState.modelIdx > 0 {
			a.modelState.modelIdx--
		} else if len(cat) > 0 {
			a.modelState.modelIdx = len(cat) - 1
		}
		a.modelState.effortIdx = 0
		return a, nil
	case tea.KeyDown:
		_, cat := a.modelCatalog()
		if len(cat) > 0 && a.modelState.modelIdx < len(cat)-1 {
			a.modelState.modelIdx++
		} else {
			a.modelState.modelIdx = 0
		}
		a.modelState.effortIdx = 0
		return a, nil
	case tea.KeyRunes:
		a.modelState.filter += string(m.Runes)
		a.modelState.modelIdx = 0
		a.modelState.scroll = 0
		return a, nil
	case tea.KeySpace:
		a.modelState.filter += " "
		a.modelState.modelIdx = 0
		a.modelState.scroll = 0
		return a, nil
	case tea.KeyBackspace:
		a.modelState.filter = trimLastRune(a.modelState.filter)
		a.modelState.modelIdx = 0
		a.modelState.scroll = 0
		return a, nil
	}
	return a, nil
}

func (a *App) commitModel() tea.Cmd {
	p, catalog := a.modelCatalog()
	midx := clampIdx(a.modelState.modelIdx, len(catalog))

	var model string
	if len(catalog) == 0 {
		model = a.cfg.Model
	} else {
		model = catalog[midx].ID
	}
	efforts := modelEfforts(catalog, midx)
	eidx := clampIdx(a.modelState.effortIdx, len(efforts))
	effort := ""
	if len(efforts) > 0 {
		effort = efforts[eidx]
	}

	if a.modelState.scope == "session" {
		a.state.Model = model
		a.state.Provider = p
		a.state.Effort = effort
		_ = config.SaveState(a.state)
		a.settings.Provider = p
		a.settings.Model = model
		a.settings.Effort = effort
	} else {
		scope := config.ScopeProject
		if a.modelState.scope == "global" {
			scope = config.ScopeGlobal
		}
		if err := config.Mutate(scope, a.workdir, func(s *config.Settings) error {
			s.Provider = p
			s.Model = model
			s.Effort = effort
			return nil
		}); err != nil {
			a.modelState.errorMsg = err.Error()
			return nil
		}
		if err := a.reloadSettings(); err != nil {
			a.modelState.errorMsg = err.Error()
			return nil
		}
	}

	a.cfg.Provider = p
	a.cfg.Model = model
	a.pop()
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
