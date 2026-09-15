package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/models"
)

// modelViewState tracks the /model picker UI.
type modelViewState struct {
	providerIdx int
	modelIdx    int
	effortIdx   int
	scope       string // session | global | project
	errorMsg    string
}

var modelHeader = lipgloss.NewStyle().Bold(true).Underline(true)

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
	return nil
}

func (a *App) modelView() string {
	providers := a.providerNames()
	pidx := clampIdx(a.modelState.providerIdx, len(providers))
	p := providers[pidx]
	catalog := a.catalogFor(p)
	midx := clampIdx(a.modelState.modelIdx, len(catalog))
	efforts := modelEfforts(catalog, midx)
	eidx := clampIdx(a.modelState.effortIdx, len(efforts))

	var b strings.Builder
	b.WriteString(modelHeader.Render("Model & Provider") + "\n\n")

	var tabs []string
	for i, name := range providers {
		glyph := "○"
		if i == pidx {
			glyph = "●"
		}
		tab := name + " " + glyph
		if i == pidx {
			tab = lipgloss.NewStyle().Bold(true).Render(tab)
		}
		tabs = append(tabs, tab)
	}
	b.WriteString(strings.Join(tabs, "   ") + "\n\n")

	if len(catalog) == 0 {
		b.WriteString("   (no models in profile; type or import a model id)\n")
	} else {
		for i, m := range catalog {
			prefix := "   "
			if i == midx {
				prefix = " > "
			}
			line := prefix + m.ID
			if m.ID == a.cfg.Model && p == a.cfg.Provider {
				line += "  (current)"
			}
			b.WriteString(line + "\n")
		}
	}

	if len(efforts) == 0 {
		b.WriteString("\neffort: unavailable (custom provider)\n")
	} else {
		b.WriteString("\neffort: ")
		for i, e := range efforts {
			if i == eidx {
				b.WriteString(lipgloss.NewStyle().Bold(true).Render("[" + e + "]"))
			} else {
				b.WriteString(" " + e)
			}
			b.WriteString(" ")
		}
		b.WriteString("\n")
	}
	b.WriteString("scope:   " + a.modelState.scope + "\n")

	if a.modelState.errorMsg != "" {
		b.WriteString("\nerror: " + a.modelState.errorMsg + "\n")
	}
	b.WriteString("\nkeys: ←→ provider · ↑↓ model · e effort · s scope · c credentials · enter set · esc cancel\n")
	return b.String()
}

func (a *App) handleModelKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.String() {
	case "esc":
		a.pop()
		return a, nil
	case "left", "h":
		a.modelState.providerIdx = (a.modelState.providerIdx - 1 + len(a.providerNames())) % len(a.providerNames())
		a.modelState.modelIdx = 0
		a.modelState.effortIdx = 0
		return a, nil
	case "right", "l":
		a.modelState.providerIdx = (a.modelState.providerIdx + 1) % len(a.providerNames())
		a.modelState.modelIdx = 0
		a.modelState.effortIdx = 0
		return a, nil
	case "up", "k":
		cat := a.catalogFor(a.providerNames()[a.modelState.providerIdx])
		if a.modelState.modelIdx > 0 {
			a.modelState.modelIdx--
		} else if len(cat) > 0 {
			a.modelState.modelIdx = len(cat) - 1
		}
		a.modelState.effortIdx = 0
		return a, nil
	case "down", "j":
		cat := a.catalogFor(a.providerNames()[a.modelState.providerIdx])
		if len(cat) > 0 && a.modelState.modelIdx < len(cat)-1 {
			a.modelState.modelIdx++
		} else {
			a.modelState.modelIdx = 0
		}
		a.modelState.effortIdx = 0
		return a, nil
	case "e":
		cat := a.catalogFor(a.providerNames()[a.modelState.providerIdx])
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

func (a *App) commitModel() tea.Cmd {
	providers := a.providerNames()
	pidx := clampIdx(a.modelState.providerIdx, len(providers))
	p := providers[pidx]
	catalog := a.catalogFor(p)
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
