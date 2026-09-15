package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/models"
	"github.com/vulnetix/signet/internal/provider"
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
	providers := provider.Names()
	pidx := indexOfString(providers, a.cfg.Provider)
	if pidx < 0 {
		pidx = 0
	}
	catalog := models.Catalog(providers[pidx])
	midx := indexOfModel(catalog, a.cfg.Model)
	if midx < 0 {
		midx = 0
	}
	efforts := catalog[midx].Efforts
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
	providers := provider.Names()
	pidx := a.modelState.providerIdx
	if pidx < 0 || pidx >= len(providers) {
		pidx = 0
	}
	p := providers[pidx]
	catalog := models.Catalog(p)
	midx := a.modelState.modelIdx
	if midx < 0 || midx >= len(catalog) {
		midx = 0
	}
	efforts := catalog[midx].Efforts
	eidx := a.modelState.effortIdx
	if eidx < 0 || eidx >= len(efforts) {
		eidx = 0
	}

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

	b.WriteString("\neffort: ")
	for i, e := range efforts {
		if i == eidx {
			b.WriteString(lipgloss.NewStyle().Bold(true).Render("[" + e + "]"))
		} else {
			b.WriteString(" " + e)
		}
		b.WriteString(" ")
	}
	b.WriteString("\nscope:   " + a.modelState.scope + "\n")

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
		a.modelState.providerIdx = (a.modelState.providerIdx - 1 + len(provider.Names())) % len(provider.Names())
		a.modelState.modelIdx = 0
		a.modelState.effortIdx = 0
		return a, nil
	case "right", "l":
		a.modelState.providerIdx = (a.modelState.providerIdx + 1) % len(provider.Names())
		a.modelState.modelIdx = 0
		a.modelState.effortIdx = 0
		return a, nil
	case "up", "k":
		cat := models.Catalog(provider.Names()[a.modelState.providerIdx])
		if a.modelState.modelIdx > 0 {
			a.modelState.modelIdx--
		} else {
			a.modelState.modelIdx = len(cat) - 1
		}
		a.modelState.effortIdx = 0
		return a, nil
	case "down", "j":
		cat := models.Catalog(provider.Names()[a.modelState.providerIdx])
		if a.modelState.modelIdx < len(cat)-1 {
			a.modelState.modelIdx++
		} else {
			a.modelState.modelIdx = 0
		}
		a.modelState.effortIdx = 0
		return a, nil
	case "e":
		cat := models.Catalog(provider.Names()[a.modelState.providerIdx])
		efforts := cat[a.modelState.modelIdx].Efforts
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
	providers := provider.Names()
	pidx := a.modelState.providerIdx
	if pidx < 0 || pidx >= len(providers) {
		pidx = 0
	}
	p := providers[pidx]
	catalog := models.Catalog(p)
	midx := a.modelState.modelIdx
	if midx < 0 || midx >= len(catalog) {
		midx = 0
	}
	model := catalog[midx].ID
	efforts := catalog[midx].Efforts
	eidx := a.modelState.effortIdx
	if eidx < 0 || eidx >= len(efforts) {
		eidx = 0
	}
	effort := efforts[eidx]

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
