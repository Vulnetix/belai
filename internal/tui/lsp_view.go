package tui

import (
	"fmt"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/lsp"
	"github.com/vulnetix/belai/internal/tui/components"
)

type lspViewState struct {
	selected int
	mode     string // "" | "install" | "install-running"
	install  *lsp.Language
}

func (a *App) lspView() string {
	w := a.contentWidth()
	var b strings.Builder
	b.WriteString(components.SectionHeader("Language servers", "esc back", w))
	if runtime.GOOS == "windows" {
		b.WriteString(components.MutedStyle.Render("Language servers are not supported on Windows in this release."))
		return lipgloss.NewStyle().Padding(1).Render(b.String())
	}

	rows := a.lspRows()
	for i, row := range rows {
		selected := i == a.lspState.selected
		label := fmt.Sprintf("%-24s", row.label)
		value := fmt.Sprintf("%-20s", row.value)
		if selected {
			label = components.AccentStyle.Bold(true).Render(label)
			value = components.EmphStyle.Render(value)
		} else {
			label = components.MutedStyle.Render(label)
		}
		b.WriteString(components.Cursor(selected) + label + value + "\n")
		if row.notes != "" {
			note := components.MutedStyle.Render("    " + row.notes)
			b.WriteString(note + "\n")
		}
	}

	if a.lspState.mode == "install" && a.lspState.install != nil {
		b.WriteString("\n" + components.EmphStyle.Render("Run:") + "\n")
		b.WriteString(strings.Join(a.lspState.install.Install, " ") + "\n")
		b.WriteString(components.HelpBar("y", "run", "n/esc", "cancel") + "\n")
	} else {
		b.WriteString("\n" + components.HelpBar(
			"↑↓", "move", "space", "toggle", "x", "unset", "i", "install", "r", "re-detect", "esc", "back") + "\n")
	}
	return lipgloss.NewStyle().Padding(1).Render(b.String())
}

type lspRow struct {
	lang    *lsp.Language
	label   string
	value   string
	notes   string
	enabled bool
}

func (a *App) lspRows() []lspRow {
	a.lspDetect.mu.Lock()
	defer a.lspDetect.mu.Unlock()

	if a.lspDetect.langs == nil {
		a.lspDetect.langs = lsp.Languages()
	}
	var detected, fallback map[string]bool
	if a.lspDetect.found != nil {
		detected = a.lspDetect.found
	}
	if a.lspDetect.fallback != nil {
		fallback = a.lspDetect.fallback
	}

	rows := make([]lspRow, 0, len(a.lspDetect.langs))
	for i := range a.lspDetect.langs {
		lang := &a.lspDetect.langs[i]
		on, explicit := a.settings.LSPLanguageEnabled(lang.ID)
		// Auto means enabled unless explicitly off.
		enabled := !explicit || on

		glyph := "·"
		switch {
		case detected[lang.ID] && enabled:
			glyph = "●"
		case detected[lang.ID] && !enabled:
			glyph = "○"
		case !detected[lang.ID] && fallback[lang.ID] && enabled:
			glyph = "◐"
		case a.lspDetect.inFlight:
			glyph = "⋯"
		}

		value := glyph + " " + lang.Display
		label := lang.Server
		if label == "" {
			label = "none"
		}
		note := lang.Notes
		if note == "" && detected[lang.ID] {
			note = "detected"
		}
		rows = append(rows, lspRow{
			lang:    lang,
			label:   value,
			value:   label,
			notes:   note,
			enabled: enabled,
		})
	}
	return rows
}

func (a *App) handleLSPKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.lspState.mode == "install" {
		switch m.String() {
		case "n", "esc":
			a.lspState.mode = ""
			a.lspState.install = nil
			return a, nil
		case "y":
			if a.lspState.install != nil {
				cmd := a.runLSPInstall(*a.lspState.install)
				a.lspState.mode = "install-running"
				a.lspState.install = nil
				return a, cmd
			}
		}
		a.lspState.mode = ""
		a.lspState.install = nil
		return a, nil
	}

	rows := a.lspRows()
	switch m.String() {
	case "up", "k":
		if a.lspState.selected > 0 {
			a.lspState.selected--
		}
	case "down", "j":
		if a.lspState.selected < len(rows)-1 {
			a.lspState.selected++
		}
	case "esc":
		a.pop()
	case " ":
		if a.lspState.selected < len(rows) {
			row := rows[a.lspState.selected]
			a.lspToggle(row.lang.ID, !row.enabled)
		}
	case "x":
		if a.lspState.selected < len(rows) {
			row := rows[a.lspState.selected]
			a.lspToggle(row.lang.ID, true) // unset: delete key
		}
	case "i":
		if a.lspState.selected < len(rows) {
			row := rows[a.lspState.selected]
			if row.lang.Install != nil && len(row.lang.Install) > 0 && !a.lspDetect.found[row.lang.ID] {
				a.lspState.mode = "install"
				a.lspState.install = row.lang
			}
		}
	case "r":
		a.invalidateLSPDetect()
		return a, a.enterLSP()
	}
	return a, nil
}

// lspToggle writes an explicit true/false into settings.LSP.Languages, or
// removes the key when unset is requested.
func (a *App) lspToggle(id string, on bool) {
	scope := a.settingsState.scope
	if scope == "" {
		scope = config.ScopeProject
	}
	if scope == config.ScopeGlobal {
		_ = config.Mutate(config.ScopeGlobal, "", func(s *config.Settings) error {
			if s.LSP == nil {
				s.LSP = &config.LSPSettings{}
			}
			if s.LSP.Languages == nil {
				s.LSP.Languages = map[string]bool{}
			}
			if on {
				delete(s.LSP.Languages, id)
			} else {
				s.LSP.Languages[id] = false
			}
			return nil
		})
	} else {
		_ = config.Mutate(config.ScopeProject, a.workdir, func(s *config.Settings) error {
			if s.LSP == nil {
				s.LSP = &config.LSPSettings{}
			}
			if s.LSP.Languages == nil {
				s.LSP.Languages = map[string]bool{}
			}
			if on {
				delete(s.LSP.Languages, id)
			} else {
				s.LSP.Languages[id] = false
			}
			return nil
		})
	}
	// Reload settings into the running app.
	merged, _ := config.LoadMerged(a.workdir)
	a.settings = merged
}

// runLSPInstall runs the install command in the background and invalidates the
// probe cache when it finishes. It is intentionally minimal: real installs are
// run in a shell by the user.
func (a *App) runLSPInstall(lang lsp.Language) tea.Cmd {
	return func() tea.Msg {
		// Actual install is intentionally manual per the security model.
		a.invalidateLSPDetect()
		return lspProbeMsg{probedAt: time.Now()}
	}
}
