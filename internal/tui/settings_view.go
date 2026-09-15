package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/signet/internal/config"
)

// settingsViewState tracks the settings browser UI.
type settingsViewState struct {
	selected int
	scope    config.Scope
	editMode bool
	errorMsg string
}

// settingsRow is one declarative settings-browser row.
type settingsRow struct {
	key   string
	label string
	kind  string   // toggle | choose | text | submenu
	opts  []string // choose options
	value string   // rendered effective value
	src   string   // provenance label
}

var settingsHeader = lipgloss.NewStyle().Bold(true).Underline(true)

func (a *App) settingsView() string {
	rows := a.settingsRows()
	var b strings.Builder
	b.WriteString(settingsHeader.Render("Settings") + "\n\n")

	scopeName := string(a.settingsState.scope)
	path := config.ProjectSettingsPath(a.workdir)
	if a.settingsState.scope == config.ScopeGlobal {
		p, _ := config.GlobalSettingsPath()
		path = p
	}
	b.WriteString(fmt.Sprintf("scope: %s  (%s)\n\n", scopeName, path))

	for i, row := range rows {
		prefix := "  "
		if i == a.settingsState.selected {
			prefix = "> "
		}
		b.WriteString(fmt.Sprintf("%s%-18s %-20s effective: %-20s (%s)\n", prefix, row.label, row.value, row.value, row.src))
		if row.kind == "submenu" {
			b.WriteString(strings.Repeat(" ", len(prefix)) + "                                                          →\n")
		}
	}

	if a.settingsState.errorMsg != "" {
		b.WriteString("\nerror: " + a.settingsState.errorMsg + "\n")
	}

	if a.settingsState.editMode {
		b.WriteString("\n" + a.editor.View() + "\n")
		b.WriteString("\nkeys: enter save · esc cancel\n")
	} else {
		b.WriteString("\nkeys: ↑↓ move · space/enter edit/open · x unset · s scope · esc back\n")
	}
	return b.String()
}

func (a *App) settingsRows() []settingsRow {
	s := a.settings
	origin := a.eff.Origin

	providerVal := s.Provider
	if providerVal == "" {
		providerVal = "—"
	}
	modelVal := s.Model
	if modelVal == "" {
		modelVal = "—"
	}
	effortVal := s.Effort
	if effortVal == "" {
		effortVal = "—"
	}
	cavemanVal := "off"
	if s.Caveman != nil && *s.Caveman {
		cavemanVal = "on"
	}
	retentionVal := "28 days"
	if s.SessionRetentionDays != nil {
		retentionVal = fmt.Sprintf("%d days", *s.SessionRetentionDays)
	}
	bannerVal := "on"
	if s.UI != nil && s.UI.Banner != nil {
		bannerVal = boolLabel(*s.UI.Banner)
	}
	showNamesVal := boolLabel(s.SessionNamesVisible())
	permsVal := fmt.Sprintf("%d allow · %d ask · %d deny", len(s.Permissions.Allow), len(s.Permissions.Ask), len(s.Permissions.Deny))

	return []settingsRow{
		{key: "provider", label: "provider", kind: "text", value: providerVal, src: sourceLabel(origin["provider"])},
		{key: "model", label: "model", kind: "text", value: modelVal, src: sourceLabel(origin["model"])},
		{key: "effort", label: "effort", kind: "choose", opts: []string{"low", "medium", "high"}, value: effortVal, src: sourceLabel(origin["effort"])},
		{key: "caveman", label: "caveman", kind: "toggle", value: cavemanVal, src: sourceLabel(origin["caveman"])},
		{key: "session_retention_days", label: "session retention", kind: "text", value: retentionVal, src: sourceLabel(origin["session_retention_days"])},
		{key: "banner", label: "banner", kind: "toggle", value: bannerVal, src: sourceLabel(origin["ui"])},
		{key: "show_session_names", label: "session names", kind: "toggle", value: showNamesVal, src: sourceLabel(origin["show_session_names"])},
		{key: "permissions", label: "permissions", kind: "submenu", value: permsVal, src: sourceLabel(origin["permissions"])},
	}
}

func sourceLabel(src config.Source) string {
	if src == "" {
		return "default"
	}
	return string(src)
}

func boolLabel(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func (a *App) handleSettingsKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.settingsState.editMode {
		switch m.String() {
		case "esc":
			a.settingsState.editMode = false
			a.settingsState.errorMsg = ""
			a.editor.Masked = false
			a.editor.Reset()
			return a, nil
		case "enter":
			rows := a.settingsRows()
			if a.settingsState.selected >= len(rows) {
				a.settingsState.editMode = false
				return a, nil
			}
			row := rows[a.settingsState.selected]
			err := a.commitTextRow(row, a.editor.Value())
			a.editor.Masked = false
			a.editor.Reset()
			if err != nil {
				a.settingsState.errorMsg = err.Error()
				return a, nil
			}
			a.settingsState.editMode = false
			a.settingsState.errorMsg = ""
			return a, nil
		default:
			cmd := a.editor.Update(m)
			return a, cmd
		}
	}

	switch m.String() {
	case "up", "k":
		if a.settingsState.selected > 0 {
			a.settingsState.selected--
		}
		return a, nil
	case "down", "j":
		if a.settingsState.selected < len(a.settingsRows())-1 {
			a.settingsState.selected++
		}
		return a, nil
	case "esc":
		a.pop()
		return a, nil
	case "s":
		if a.settingsState.scope == config.ScopeGlobal {
			a.settingsState.scope = config.ScopeProject
		} else {
			a.settingsState.scope = config.ScopeGlobal
		}
		return a, nil
	case "x":
		rows := a.settingsRows()
		if a.settingsState.selected < len(rows) {
			row := rows[a.settingsState.selected]
			if err := a.unsetSetting(row.key); err != nil {
				a.settingsState.errorMsg = err.Error()
			} else {
				a.settingsState.errorMsg = ""
			}
		}
		return a, nil
	case " ", "enter":
		rows := a.settingsRows()
		if a.settingsState.selected >= len(rows) {
			return a, nil
		}
		row := rows[a.settingsState.selected]
		switch row.kind {
		case "submenu":
			return a, a.push(viewPermissions)
		case "toggle":
			if err := a.cycleToggle(row.key); err != nil {
				a.settingsState.errorMsg = err.Error()
			} else {
				a.settingsState.errorMsg = ""
			}
			return a, nil
		case "choose":
			if err := a.cycleChoice(row.key, row.opts); err != nil {
				a.settingsState.errorMsg = err.Error()
			} else {
				a.settingsState.errorMsg = ""
			}
			return a, nil
		case "text":
			a.settingsState.editMode = true
			a.settingsState.errorMsg = ""
			a.editor.Masked = false
			a.editor.SetValue(a.rawValue(row.key))
			_ = a.editor.Focus()
			return a, nil
		}
	}

	cmd := a.editor.Update(m)
	return a, cmd
}

func (a *App) rawValue(key string) string {
	switch key {
	case "provider":
		return a.settings.Provider
	case "model":
		return a.settings.Model
	case "session_retention_days":
		if a.settings.SessionRetentionDays != nil {
			return strconv.Itoa(*a.settings.SessionRetentionDays)
		}
		return ""
	}
	return ""
}

func (a *App) commitTextRow(row settingsRow, raw string) error {
	val := strings.TrimSpace(raw)
	switch row.key {
	case "provider":
		if val == "" {
			return a.unsetSetting("provider")
		}
		if !a.isProviderName(val) {
			return fmt.Errorf("unknown provider %q", val)
		}
		return a.mutateSetting(func(s *config.Settings) { s.Provider = val })
	case "model":
		return a.mutateSetting(func(s *config.Settings) { s.Model = val })
	case "session_retention_days":
		if val == "" {
			return a.unsetSetting("session_retention_days")
		}
		n, err := strconv.Atoi(val)
		if err != nil || n <= 0 {
			return fmt.Errorf("session retention must be a positive integer")
		}
		return a.mutateSetting(func(s *config.Settings) { s.SessionRetentionDays = &n })
	}
	return fmt.Errorf("cannot edit %q", row.key)
}

func (a *App) cycleToggle(key string) error {
	return a.mutateSetting(func(s *config.Settings) {
		switch key {
		case "caveman":
			s.Caveman = nextBool(s.Caveman)
		case "banner":
			if s.UI == nil {
				s.UI = &config.UISettings{}
			}
			s.UI.Banner = nextBool(s.UI.Banner)
		case "show_session_names":
			s.ShowSessionNames = nextBool(s.ShowSessionNames)
		}
	})
}

func (a *App) cycleChoice(key string, opts []string) error {
	return a.mutateSetting(func(s *config.Settings) {
		if key != "effort" {
			return
		}
		cur := s.Effort
		idx := -1
		for i, o := range opts {
			if o == cur {
				idx = i
				break
			}
		}
		s.Effort = opts[(idx+1)%len(opts)]
	})
}

func (a *App) unsetSetting(key string) error {
	return a.mutateSetting(func(s *config.Settings) {
		switch key {
		case "provider":
			s.Provider = ""
		case "model":
			s.Model = ""
		case "effort":
			s.Effort = ""
		case "caveman":
			s.Caveman = nil
		case "session_retention_days":
			s.SessionRetentionDays = nil
		case "banner":
			if s.UI != nil {
				s.UI.Banner = nil
			}
		case "show_session_names":
			s.ShowSessionNames = nil
		}
	})
}

func (a *App) mutateSetting(fn func(*config.Settings)) error {
	if err := config.Mutate(a.settingsState.scope, a.workdir, func(s *config.Settings) error {
		fn(s)
		return nil
	}); err != nil {
		return err
	}
	return a.reloadSettings()
}

func nextBool(b *bool) *bool {
	if b == nil {
		t := true
		return &t
	}
	if *b {
		f := false
		return &f
	}
	return nil
}

func (a *App) isProviderName(name string) bool {
	for _, n := range a.providerNames() {
		if n == name {
			return true
		}
	}
	return false
}
