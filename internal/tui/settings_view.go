package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/lsp"
	"github.com/vulnetix/signet/internal/tui/components"
)

// settingsViewState tracks the settings browser UI.
type settingsViewState struct {
	selected int
	scope    config.Scope
	// scopeChosen records that the user picked the scope with `s` this
	// session, so reopening /settings keeps it instead of snapping back to
	// project.
	scopeChosen bool
	editMode    bool
	errorMsg    string
	// notice explains a saved edit that a higher settings layer shadows.
	notice string
}

// settingsRow is one declarative settings-browser row.
type settingsRow struct {
	key   string
	label string
	kind  string   // toggle | choose | text | submenu | pick
	opts  []string // choose options
	value string   // rendered effective value
	src   string   // provenance label
	// disabled greys the row out and makes space/enter skip it. It is how a
	// row that only means something under another row's setting — an effort
	// with reasoning off — stays visible without being changeable.
	disabled bool
}

func (a *App) settingsView() string {
	rows := a.settingsRows()
	w := a.contentWidth()
	var b strings.Builder
	b.WriteString(components.SectionHeader("Settings", "esc back", w))

	path := config.ProjectSettingsPath(a.workdir)
	scope := string(a.settingsState.scope)
	if a.settingsState.scope == config.ScopeGlobal {
		p, _ := config.GlobalSettingsPath()
		path = p
	}
	if scope == "" {
		// The view opens before a scope has been chosen; it writes project.
		scope = string(config.ScopeProject)
	}
	b.WriteString(components.Chip(scope, components.ColorTealSoft) +
		"  " + components.MutedStyle.Render(path) + "\n\n")

	for i, row := range rows {
		selected := i == a.settingsState.selected
		label := fmt.Sprintf("%-20s", row.label)
		value := fmt.Sprintf("%-27s ", row.value)
		if row.kind == "submenu" {
			value = fmt.Sprintf("%-25s → ", row.value)
		}
		if selected {
			label = components.AccentStyle.Bold(true).Render(label)
			value = components.EmphStyle.Render(value)
		} else {
			label = components.MutedStyle.Render(label)
		}
		b.WriteString(components.Cursor(selected) + label + value +
			components.MutedStyle.Render(row.src) + "\n")
	}

	if a.settingsState.errorMsg != "" {
		b.WriteString("\n" + components.DangerStyle.Render("✗ "+a.settingsState.errorMsg) + "\n")
	} else if a.settingsState.notice != "" {
		b.WriteString("\n" + components.WarnStyle.Render("! "+a.settingsState.notice) + "\n")
	}

	if a.settingsState.editMode {
		b.WriteString("\n" + a.renderFieldEditor("edit", w) + "\n")
		b.WriteString("\n" + components.HelpBar("enter", "save", "esc", "cancel") + "\n")
	} else {
		b.WriteString("\n" + components.HelpBar(
			"↑↓", "move", "space", "edit/open", "x", "unset", "s", "scope", "esc", "back") + "\n")
	}
	return lipgloss.NewStyle().Padding(1).Render(b.String())
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
	readOnlyVal := boolLabel(s.ReadOnlyEnabled())
	retentionVal := "28 days"
	if s.SessionRetentionDays != nil {
		retentionVal = fmt.Sprintf("%d days", *s.SessionRetentionDays)
	}
	bannerVal := "shown"
	if s.UI != nil && s.UI.Banner != nil {
		bannerVal = showLabel(*s.UI.Banner)
	}
	colorsVal := boolLabel(s.ColorsEnabled())
	spinnerVal := showLabel(s.SpinnerEnabled())
	reasoningVal := showLabel(s.ReasoningVisible())
	toolCallsVal := showLabel(s.ToolCallsVisible())
	editsVal := showLabel(s.EditsVisible())
	todosVal := showLabel(s.TodosVisible())
	mouseVal := boolLabel(s.MouseEnabled())
	showNamesVal := showLabel(s.SessionNamesVisible())
	updateCheckVal := boolLabel(s.UpdateCheckEnabled())
	permsVal := fmt.Sprintf("%d allow · %d ask · %d deny", len(s.Permissions.Allow), len(s.Permissions.Ask), len(s.Permissions.Deny))
	maxAgentsVal := strconv.Itoa(config.DefaultMaxAgents)
	if s.Resilience != nil && s.Resilience.MaxAgents != 0 {
		maxAgentsVal = strconv.Itoa(s.Resilience.MaxAgents)
	}
	planExploreVal := boolLabel(s.PlanExploreEnabled())
	goalExploreVal := boolLabel(s.GoalExploreEnabled())

	return []settingsRow{
		{key: "provider", label: "provider", kind: "text", value: providerVal, src: sourceLabel(origin["provider"])},
		{key: "model", label: "model", kind: "text", value: modelVal, src: sourceLabel(origin["model"])},
		{key: "effort", label: "effort", kind: "choose", opts: []string{"low", "medium", "high"}, value: effortVal, src: sourceLabel(origin["effort"])},
		{key: "caveman", label: "caveman", kind: "toggle", value: cavemanVal, src: sourceLabel(origin["caveman"])},
		{key: "read_only", label: "read-only tools", kind: "toggle", value: readOnlyVal, src: sourceLabel(origin["read_only"])},
		{key: "session_retention_days", label: "session retention", kind: "text", value: retentionVal, src: sourceLabel(origin["session_retention_days"])},
		{key: "banner", label: "banner", kind: "toggle", value: bannerVal, src: sourceLabel(origin["ui"])},
		{key: "colors", label: "colours", kind: "toggle", value: colorsVal, src: sourceLabel(origin["ui"])},
		{key: "spinner", label: "spinner", kind: "toggle", value: spinnerVal, src: sourceLabel(origin["ui"])},
		{key: "show_reasoning", label: "reasoning", kind: "toggle", value: reasoningVal, src: sourceLabel(origin["ui"])},
		{key: "show_tool_calls", label: "tool calls", kind: "toggle", value: toolCallsVal, src: sourceLabel(origin["ui"])},
		{key: "show_edits", label: "file edits", kind: "toggle", value: editsVal, src: sourceLabel(origin["ui"])},
		{key: "show_internal_work", label: "internal work", kind: "choose", opts: []string{"hidden", "decisions", "security", "all"}, value: s.InternalWorkLevel(), src: sourceLabel(origin["ui"])},
		{key: "show_todos", label: "todo panel", kind: "toggle", value: todosVal, src: sourceLabel(origin["ui"])},
		{key: "mouse", label: "mouse capture", kind: "toggle", value: mouseVal, src: sourceLabel(origin["ui"])},
		{key: "show_session_names", label: "session names", kind: "toggle", value: showNamesVal, src: sourceLabel(origin["show_session_names"])},
		{key: "update_check", label: "update check", kind: "toggle", value: updateCheckVal, src: sourceLabel(origin["update_check"])},
		{key: "max_agents", label: "max agents", kind: "text", value: maxAgentsVal, src: sourceLabel(origin["resilience"])},
		{key: "plan_explore", label: "plan explore", kind: "toggle", value: planExploreVal, src: sourceLabel(origin["resilience"])},
		{key: "goal_explore", label: "goal explore", kind: "toggle", value: goalExploreVal, src: sourceLabel(origin["resilience"])},
		{key: "permissions", label: "permissions", kind: "submenu", value: permsVal, src: sourceLabel(origin["permissions"])},
		{key: "lsp", label: "language servers", kind: "submenu", value: lspSummary(a), src: sourceLabel(origin["lsp"])},
	}
}

func lspSummary(a *App) string {
	if a.lspDetect.langs == nil {
		return "…"
	}
	installed := 0
	for _, v := range a.lspDetect.found {
		if v {
			installed++
		}
	}
	total := len(a.lspDetect.langs)
	if total == 0 {
		total = len(lsp.Languages())
	}
	return fmt.Sprintf("on · %d of %d detected", installed, total)
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

// showLabel is the display-toggle wording: a row that controls whether
// something is shown says shown/hidden, never on/off. Behaviour toggles keep
// boolLabel.
func showLabel(v bool) string {
	if v {
		return "shown"
	}
	return "hidden"
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
			a.settingsState.notice = a.shadowNotice(row.key)
			if row.key == "provider" || row.key == "model" {
				return a, a.syncProviderFromSettings()
			}
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
		a.settingsState.scopeChosen = true
		a.settingsState.notice = ""
		return a, nil
	case "x":
		rows := a.settingsRows()
		if a.settingsState.selected < len(rows) {
			row := rows[a.settingsState.selected]
			if err := a.unsetSetting(row.key); err != nil {
				a.settingsState.errorMsg = err.Error()
			} else {
				a.settingsState.errorMsg = ""
				if row.key == "provider" || row.key == "model" {
					return a, a.syncProviderFromSettings()
				}
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
			switch row.key {
			case "lsp":
				return a, a.push(viewLSP)
			default:
				return a, a.push(viewPermissions)
			}
		case "toggle":
			if err := a.cycleToggle(row.key); err != nil {
				a.settingsState.errorMsg = err.Error()
			} else {
				a.settingsState.errorMsg = ""
				a.settingsState.notice = a.shadowNotice(row.key)
			}
			return a, nil
		case "choose":
			if err := a.cycleChoice(row.key, row.opts); err != nil {
				a.settingsState.errorMsg = err.Error()
			} else {
				a.settingsState.errorMsg = ""
				a.settingsState.notice = a.shadowNotice(row.key)
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
	case "max_agents":
		if a.settings.Resilience != nil && a.settings.Resilience.MaxAgents != 0 {
			return strconv.Itoa(a.settings.Resilience.MaxAgents)
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
		// A provider change invalidates the old provider's model, matching the
		// /model screen's provider-cycle tail.
		return a.mutateSetting(func(s *config.Settings) { s.Provider = val; s.Model = "" })
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
	case "max_agents":
		if val == "" {
			return a.unsetSetting("max_agents")
		}
		n, err := strconv.Atoi(val)
		if err != nil || n <= 0 {
			return fmt.Errorf("max agents must be a positive integer")
		}
		return a.mutateSetting(func(s *config.Settings) {
			if s.Resilience == nil {
				s.Resilience = &config.ResilienceSettings{}
			}
			s.Resilience.MaxAgents = n
		})
	}
	return fmt.Errorf("cannot edit %q", row.key)
}

func (a *App) cycleToggle(key string) error {
	return a.mutateSetting(func(s *config.Settings) {
		switch key {
		case "caveman":
			s.Caveman = nextBool(s.Caveman)
		case "read_only":
			s.ReadOnly = nextBool(s.ReadOnly)
		case "banner":
			if s.UI == nil {
				s.UI = &config.UISettings{}
			}
			s.UI.Banner = nextBool(s.UI.Banner)
		case "colors":
			if s.UI == nil {
				s.UI = &config.UISettings{}
			}
			s.UI.Colors = nextBool(s.UI.Colors)
		case "spinner":
			if s.UI == nil {
				s.UI = &config.UISettings{}
			}
			s.UI.Spinner = nextBool(s.UI.Spinner)
		case "show_reasoning":
			if s.UI == nil {
				s.UI = &config.UISettings{}
			}
			s.UI.ShowReasoning = nextBool(s.UI.ShowReasoning)
		case "show_tool_calls":
			if s.UI == nil {
				s.UI = &config.UISettings{}
			}
			s.UI.ShowToolCalls = nextBool(s.UI.ShowToolCalls)
		case "show_edits":
			if s.UI == nil {
				s.UI = &config.UISettings{}
			}
			s.UI.ShowEdits = nextBool(s.UI.ShowEdits)
		case "show_todos":
			if s.UI == nil {
				s.UI = &config.UISettings{}
			}
			s.UI.ShowTodos = nextBool(s.UI.ShowTodos)
		case "mouse":
			if s.UI == nil {
				s.UI = &config.UISettings{}
			}
			s.UI.Mouse = nextBool(s.UI.Mouse)
		case "show_session_names":
			s.ShowSessionNames = nextBool(s.ShowSessionNames)
		case "update_check":
			s.UpdateCheck = nextBool(s.UpdateCheck)
		case "plan_explore":
			if s.Resilience == nil {
				s.Resilience = &config.ResilienceSettings{}
			}
			s.Resilience.PlanExplore = nextBool(s.Resilience.PlanExplore)
		case "goal_explore":
			if s.Resilience == nil {
				s.Resilience = &config.ResilienceSettings{}
			}
			s.Resilience.GoalExplore = nextBool(s.Resilience.GoalExplore)
		}
	})
}

func (a *App) cycleChoice(key string, opts []string) error {
	return a.mutateSetting(func(s *config.Settings) {
		switch key {
		case "effort":
			cur := s.Effort
			idx := indexOfString(opts, cur)
			s.Effort = opts[(idx+1)%len(opts)]
		case "show_internal_work":
			cur := s.InternalWorkLevel()
			idx := indexOfString(opts, cur)
			if s.UI == nil {
				s.UI = &config.UISettings{}
			}
			next := opts[(idx+1)%len(opts)]
			s.UI.ShowInternalWork = &next
		}
	})
}

func (a *App) unsetSetting(key string) error {
	return a.mutateSetting(func(s *config.Settings) {
		switch key {
		case "provider":
			s.Provider = ""
			s.Model = ""
		case "model":
			s.Model = ""
		case "effort":
			s.Effort = ""
		case "caveman":
			s.Caveman = nil
		case "read_only":
			s.ReadOnly = nil
		case "session_retention_days":
			s.SessionRetentionDays = nil
		case "banner":
			if s.UI != nil {
				s.UI.Banner = nil
			}
		case "colors":
			if s.UI != nil {
				s.UI.Colors = nil
			}
		case "spinner":
			if s.UI != nil {
				s.UI.Spinner = nil
			}
		case "show_reasoning":
			if s.UI != nil {
				s.UI.ShowReasoning = nil
			}
		case "show_tool_calls":
			if s.UI != nil {
				s.UI.ShowToolCalls = nil
			}
		case "show_edits":
			if s.UI != nil {
				s.UI.ShowEdits = nil
			}
		case "show_internal_work":
			if s.UI != nil {
				s.UI.ShowInternalWork = nil
			}
		case "show_todos":
			if s.UI != nil {
				s.UI.ShowTodos = nil
			}
		case "mouse":
			if s.UI != nil {
				s.UI.Mouse = nil
			}
		case "show_session_names":
			s.ShowSessionNames = nil
		case "update_check":
			s.UpdateCheck = nil
		case "plan_explore":
			if s.Resilience != nil {
				s.Resilience.PlanExplore = nil
			}
		case "goal_explore":
			if s.Resilience != nil {
				s.Resilience.GoalExplore = nil
			}
		case "max_agents":
			if s.Resilience != nil {
				s.Resilience.MaxAgents = 0
			}
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

// persistPref writes one project preference and reloads, so the next session
// in this project opens with it. It never touches global settings or the
// repository's own .vulnetix/settings.json, and it never reaches a session
// already running in another process — those read their settings at startup.
func (a *App) persistPref(fn func(*config.ProjectPrefs)) error {
	if err := config.MutateProjectPrefs(a.workdir, fn); err != nil {
		return err
	}
	return a.reloadSettings()
}

// prefHonestyNotice returns a system line when a just-toggled project pref did
// not resolve to the toggled value because a higher-precedence source won,
// naming that source; it returns "" when the pref stuck. Env, a CLI flag, or
// an explicit key in .vulnetix/settings.json all outrank the prefs layer.
func (a *App) prefHonestyNotice(key string, want, got bool) string {
	if want == got {
		return ""
	}
	src := string(a.eff.Origin[key])
	if src == "" {
		src = "default"
	}
	return fmt.Sprintf("%s: %s ignored — %s wins", key, boolLabel(want), src)
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

// readOnlyNotice returns the system line shown when agent mode runs with the
// read_only setting on, naming the settings layer that turned it on, or "".
// Sessions showed agent turns silently unable to write or test because a
// project file had read_only set; the notice makes that visible, and it says
// what is unaffected so the user knows goal mode is the way through.
func (a *App) readOnlyNotice() string {
	if !a.settings.ReadOnlyEnabled() {
		return ""
	}
	return fmt.Sprintf("read-only tools: on (%s) — agent mode has no Write/Edit and Bash is allowlisted; goal mode and approved plans are unaffected", sourceLabel(a.eff.Origin["read_only"]))
}

// sourceRank orders settings layers by precedence, lowest first.
var sourceRank = map[string]int{
	string(config.SourceDefault):      0,
	string(config.SourceState):        1,
	string(config.SourceGlobal):       2,
	string(config.SourceProjectPrefs): 3,
	string(config.SourceProject):      4,
	string(config.SourceEnv):          5,
	string(config.SourceFlag):         6,
}

// shadowNotice explains a just-saved /settings edit that did not take effect
// because a higher-precedence layer sets the same key — the "I changed it and
// it didn't stick" report. It reads the row's provenance after the reload, so
// it names the layer that actually won and the value in force. It returns ""
// when the edit is what the row now shows.
func (a *App) shadowNotice(key string) string {
	written := string(config.SourceProject)
	if a.settingsState.scope == config.ScopeGlobal {
		written = string(config.SourceGlobal)
	}
	for _, row := range a.settingsRows() {
		if row.key != key {
			continue
		}
		if sourceRank[row.src] > sourceRank[written] {
			return fmt.Sprintf("%s: saved to %s, but %s wins (%s) — edit it there or switch scope with s", row.label, written, row.src, strings.TrimSpace(row.value))
		}
		return ""
	}
	return ""
}
