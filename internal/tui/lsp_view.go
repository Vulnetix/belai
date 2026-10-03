package tui

import (
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/lsp"
	"github.com/vulnetix/belai/internal/tui/components"
)

type lspViewState struct {
	selected int
	mode     string // "" | "install" | "install-running"
	install  *lsp.Language
	gl       glState
}

// lspChromeRows is what the screen spends outside the grouped body: the view's
// own padding (2), the header and its rule (2) and the key line.
const lspChromeRows = 5

func (a *App) lspView() string {
	w := a.contentWidth()
	var b strings.Builder
	b.WriteString(components.SectionHeader("Language servers", "esc back", w))
	if runtime.GOOS == "windows" {
		b.WriteString(components.MutedStyle.Render("Language servers are not supported on Windows in this release."))
		return lipgloss.NewStyle().Padding(1).Render(b.String())
	}

	rows := a.lspRows()
	groups := a.lspGroups(rows)
	a.lspState.selected = glSnap(groups, a.lspState.selected)

	var tail []string
	foot := glHelpLine([]glKey{
		{"↑↓", "move", 1}, {"space", "toggle", 1}, {"esc", "back", 1},
		{"[ ]", "group", 2}, {"i", "install", 2}, {"r", "re-detect", 3}, {"x", "unset", 3},
	}, w)
	if a.lspState.mode == "install" && a.lspState.install != nil {
		tail = append(tail, components.EmphStyle.Render("Run:"),
			ansi.Truncate(strings.Join(a.lspState.install.Install, " "), w, "…"))
		foot = components.HelpBar("y", "run", "n/esc", "cancel")
	}
	bodyH := 0
	if a.height > 0 {
		bodyH = max(a.height-lspChromeRows-len(tail), glMinBody)
	}
	b.WriteString(glBody(glSpec{groups: groups, cursor: a.lspState.selected}, &a.lspState.gl, w, bodyH))
	for _, l := range tail {
		b.WriteString("\n" + l)
	}
	b.WriteString("\n" + foot)
	return lipgloss.NewStyle().Padding(1).Render(b.String())
}

// lspGroups splits the languages into those found on this machine and those
// not, each row keeping its index in lspRows so every handler still addresses
// the flat list.
func (a *App) lspGroups(rows []lspRow) []glGroup {
	scope := a.settingsState.scope
	if scope == "" {
		scope = config.ScopeProject
	}
	found := glGroup{key: "found", title: "Detected"}
	missing := glGroup{key: "missing", title: "Not detected"}
	for i, r := range rows {
		src := "default"
		if !r.enabled {
			src = "off"
		}
		detail := []string{components.EmphStyle.Render(r.lang.Display) + components.MutedStyle.Render("   server: "+r.value)}
		if r.notes != "" {
			detail = append(detail, r.notes)
		}
		if !r.detected && len(r.lang.Install) > 0 {
			detail = append(detail, components.MutedStyle.Render("install: "+strings.Join(r.lang.Install, " ")+"  (i)"))
		}
		state := "on"
		if !r.enabled {
			state = "turned off"
		}
		detail = append(detail, components.MutedStyle.Render(state+" · saved to "+string(scope)))
		row := glRow{idx: i, key: r.lang.ID, label: r.glyph + " " + r.lang.Display, value: r.value, src: src, dim: !r.enabled, detail: detail, match: r.notes}
		if r.detected {
			found.rows = append(found.rows, row)
		} else {
			missing.rows = append(missing.rows, row)
		}
	}
	var groups []glGroup
	for _, g := range []glGroup{found, missing} {
		if len(g.rows) > 0 {
			groups = append(groups, g)
		}
	}
	return groups
}

type lspRow struct {
	lang     *lsp.Language
	label    string
	value    string
	notes    string
	enabled  bool
	detected bool
	glyph    string
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
			lang:     lang,
			label:    value,
			value:    label,
			notes:    note,
			enabled:  enabled,
			detected: detected[lang.ID],
			glyph:    glyph,
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
	case "up", "k", "down", "j":
		delta := 1
		if m.String() == "up" || m.String() == "k" {
			delta = -1
		}
		groups := a.lspGroups(rows)
		a.lspState.selected = glStep(groups, glSnap(groups, a.lspState.selected), delta)
	case "[", "]", "tab", "shift+tab", "left", "right":
		delta := 1
		if m.String() == "[" || m.String() == "shift+tab" || m.String() == "left" {
			delta = -1
		}
		groups := a.lspGroups(rows)
		a.lspState.selected = glStepGroup(groups, glSnap(groups, a.lspState.selected), delta)
		a.lspState.gl.scroll = 0
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
	a.applyCtlOverlay()
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
