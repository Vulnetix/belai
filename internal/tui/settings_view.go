package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/lsp"
	"github.com/vulnetix/belai/internal/tui/components"
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
	// help is the one-line description shown under the selected row. Empty
	// rows render no help line.
	help string
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

	if a.settingsState.selected < len(rows) && rows[a.settingsState.selected].help != "" {
		b.WriteString("\n" + components.MutedStyle.Render(rows[a.settingsState.selected].help) + "\n")
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
	autoCommitVal := boolLabel(s.AutoCommitPerTaskEnabled())
	testsPostEndVal := s.TestsPostEnd()
	testsScopeVal := s.TestsScope()
	testsOnFailVal := s.TestsOnFail()
	testsReportVal := boolLabel(s.TestsReportEnabled())
	testsMaxFixVal := strconv.Itoa(s.TestsMaxFixPasses())
	testsTimeoutVal := fmt.Sprintf("%ds", s.TestsTimeoutSeconds())
	testsCommandVal := "detected"
	if c := s.TestsCommand(); len(c) > 0 {
		testsCommandVal = strings.Join(c, " ")
	}
	permsVal := fmt.Sprintf("%d allow · %d ask · %d deny", len(s.Permissions.Allow), len(s.Permissions.Ask), len(s.Permissions.Deny))
	maxAgentsVal := strconv.Itoa(config.DefaultMaxAgents)
	if s.Resilience != nil && s.Resilience.MaxAgents != 0 {
		maxAgentsVal = strconv.Itoa(s.Resilience.MaxAgents)
	}
	planExploreVal := boolLabel(s.PlanExploreEnabled())
	goalExploreVal := boolLabel(s.GoalExploreEnabled())
	budgetCycleVal := fmt.Sprintf("%ds", int(s.BudgetCycle().Seconds()))
	budgetWarnVal := boolLabel(s.BudgetWarnEnabled())
	budgetsVal := fmt.Sprintf("%d set", len(s.TokenBudgets))
	intelVal := boolLabel(s.IntelEnabled())
	planLimitsVal := boolLabel(s.PlanLimitsEnabled())

	rows := []settingsRow{
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
		{key: "auto_commit_per_task", label: "auto-commit per task", kind: "toggle", value: autoCommitVal, src: sourceLabel(origin["auto_commit_per_task"]), help: "commits each completed goal's changed files as one conventional commit — global only, and a file you edited before the goal touched it is committed whole"},
		{key: "tests.post_end", label: "test pass", kind: "choose", opts: []string{"off", "goal", "goal_plan", "session"}, value: testsPostEndVal, src: sourceLabel(origin["tests"]), help: "runs detected tests at the end of a goal, plan, or session — off by default; a project layer can only turn this off"},
		{key: "tests.scope", label: "test scope", kind: "choose", opts: []string{"affected", "full"}, value: testsScopeVal, src: sourceLabel(origin["tests"]), help: "affected runs only the suites owning changed paths, falling back to the full suite"},
		{key: "tests.on_fail", label: "test on fail", kind: "choose", opts: []string{"off", "diagnose", "fix"}, value: testsOnFailVal, src: sourceLabel(origin["tests"]), help: "what happens when the test pass fails: nothing, a read-only diagnosis, or an agentic diagnose-and-fix loop"},
		{key: "tests.command", label: "test command", kind: "text", value: testsCommandVal, src: sourceLabel(origin["tests"]), help: "override the detected test command argv — user layers only; clear to return to the detected command"},
		{key: "tests.report", label: "test report", kind: "toggle", value: testsReportVal, src: sourceLabel(origin["tests"]), help: "a fast-model report on a passing test run instead of the harness-composed line"},
		{key: "tests.max_fix_passes", label: "test fix passes", kind: "text", value: testsMaxFixVal, src: sourceLabel(origin["tests"]), help: "bounds the diagnose-and-fix loop"},
		{key: "tests.timeout_seconds", label: "test timeout", kind: "text", value: testsTimeoutVal, src: sourceLabel(origin["tests"]), help: "one suite run's time limit in seconds"},
		{key: "max_agents", label: "max agents", kind: "text", value: maxAgentsVal, src: sourceLabel(origin["resilience"])},
		{key: "plan_explore", label: "plan explore", kind: "toggle", value: planExploreVal, src: sourceLabel(origin["resilience"])},
		{key: "goal_explore", label: "goal explore", kind: "toggle", value: goalExploreVal, src: sourceLabel(origin["resilience"])},
		{key: "permissions", label: "permissions", kind: "submenu", value: permsVal, src: sourceLabel(origin["permissions"])},
		{key: "lsp", label: "language servers", kind: "submenu", value: lspSummary(a), src: sourceLabel(origin["lsp"])},
		{key: "token_budgets", label: "token budgets", kind: "submenu", value: budgetsVal, src: sourceLabel(origin["token_budgets"])},
		{key: "budget_cycle_seconds", label: "budget cycle", kind: "text", value: budgetCycleVal, src: sourceLabel(origin["ui"])},
		{key: "budget_warn", label: "budget warnings", kind: "toggle", value: budgetWarnVal, src: sourceLabel(origin["ui"])},
		{key: "intel", label: "session intelligence", kind: "toggle", value: intelVal, src: sourceLabel(origin["ui"]), help: "the footer slot, the intel tab (f12) and /intel"},
		{key: "plan_limits", label: "plan limits", kind: "toggle", value: planLimitsVal, src: sourceLabel(origin["intel"]), help: "read provider rate-limit response headers into plan-limit readings — global only"},
		{key: "voice.enabled", label: "voice input", kind: "toggle", value: boolLabel(s.Voice.VoiceEnabled()), src: sourceLabel(origin["voice"]), help: "dictate into the composer (/voice); the speech model is a 32 MB download you confirm — global only"},
		{key: "voice.mode", label: "voice mode", kind: "choose", opts: []string{config.VoiceModePushToTalk, config.VoiceModeListen}, value: s.Voice.VoiceModeOr(), src: sourceLabel(origin["voice"]), help: "push_to_talk records while the key is held or toggled; listen keeps the microphone open while the composer is ready"},
		{key: "voice.delivery", label: "voice delivery", kind: "choose", opts: []string{config.VoiceDeliveryInsert, config.VoiceDeliverySubmit}, value: s.Voice.VoiceDeliveryOr(), src: sourceLabel(origin["voice"]), help: "insert leaves dictated text in the composer; submit also sends it, except text that starts with / or !, or holds an @path"},
		{key: "voice.cleanup", label: "voice cleanup", kind: "toggle", value: boolLabel(s.Voice.VoiceCleanupEnabled()), src: sourceLabel(origin["voice"]), help: "a fast-model pass that tidies the transcript before it is inserted"},
	}
	// The Jev jobs exist only while a decision backend is configured; without
	// one they are off and hidden, not greyed.
	if s.JevConfigured() {
		rows = append(rows, jevRows(s, origin)...)
	}
	return rows
}

// jevRowPrefix marks the /settings rows of the Jev jobs; the rest of the key is
// the job name.
const jevRowPrefix = "jev:"

// jevJobLabels and jevJobHelp are the /settings wording of each job.
var jevJobLabels = map[config.JevJob]string{
	config.JevBashSwap:        "jev bash swap",
	config.JevPruneCompaction: "jev compaction prune",
	config.JevToolSelection:   "jev tool selection",
	config.JevToolSearch:      "jev tool search",
	config.JevLSPTriage:       "jev lsp triage",
	config.JevOptionOrder:     "jev option order",
	config.JevExploreLocate:   "jev explore locate",
}

var jevJobHelp = map[config.JevJob]string{
	config.JevBashSwap:        "run a builtin tool instead of Bash when one is a clear match, and tell the model",
	config.JevPruneCompaction: "keep or clear tool results by relevance when compacting, before writing a summary",
	config.JevToolSelection:   "preload the tools and skills a request needs and list only the relevant skills",
	config.JevToolSearch:      "rank ToolSearch matches, dropping poor ones and adding strong ones",
	config.JevLSPTriage:       "file a board bug when another edit is unlikely to clear the language server errors",
	config.JevOptionOrder:     "put the most likely answer first, marked (Recommended), when the model asks you to choose",
	config.JevExploreLocate:   "rank the files a question is about, so explore subagents start where the code is",
}

// jevRows builds the toggle rows for the Jev jobs, one per job, in the
// documented order.
func jevRows(s config.Settings, origin map[string]config.Source) []settingsRow {
	rows := make([]settingsRow, 0, len(config.JevJobs))
	for _, j := range config.JevJobs {
		rows = append(rows, settingsRow{
			key:   jevRowPrefix + string(j),
			label: jevJobLabels[j],
			kind:  "toggle",
			value: boolLabel(s.JevJobSet(j)),
			src:   sourceLabel(origin["jev"]),
			help:  jevJobHelp[j],
		})
	}
	return rows
}

// setJevJob switches one Jev job. on nil removes the explicit switch, which
// returns the job to its default (on). The cached agent session is dropped so
// the next prompt runs with the new switch.
func (a *App) setJevJob(job config.JevJob, on *bool) error {
	err := a.mutateSetting(func(s *config.Settings) {
		if on == nil {
			if s.Jev != nil {
				delete(s.Jev.Jobs, string(job))
			}
			return
		}
		if s.Jev == nil {
			s.Jev = &config.JevSettings{}
		}
		if s.Jev.Jobs == nil {
			s.Jev.Jobs = map[string]bool{}
		}
		s.Jev.Jobs[string(job)] = *on
	})
	if err == nil {
		a.invalidateAgentSession()
	}
	return err
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
				if strings.HasPrefix(row.key, "voice.") {
					return a, a.voiceAfterSetting(row.key)
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
			case "token_budgets":
				return a, a.openBudgets()
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
			return a, a.voiceAfterSetting(row.key)
		case "choose":
			if err := a.cycleChoice(row.key, row.opts); err != nil {
				a.settingsState.errorMsg = err.Error()
			} else {
				a.settingsState.errorMsg = ""
				a.settingsState.notice = a.shadowNotice(row.key)
			}
			return a, a.voiceAfterSetting(row.key)
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
	case "budget_cycle_seconds":
		if a.settings.UI != nil && a.settings.UI.BudgetCycleSeconds != nil {
			return strconv.Itoa(*a.settings.UI.BudgetCycleSeconds)
		}
		return ""
	case "tests.command":
		if c := a.settings.TestsCommand(); len(c) > 0 {
			return strings.Join(c, " ")
		}
		return ""
	case "tests.max_fix_passes":
		if a.settings.Tests != nil && a.settings.Tests.MaxFixPasses > 0 {
			return strconv.Itoa(a.settings.Tests.MaxFixPasses)
		}
		return ""
	case "tests.timeout_seconds":
		if a.settings.Tests != nil && a.settings.Tests.TimeoutSeconds > 0 {
			return strconv.Itoa(a.settings.Tests.TimeoutSeconds)
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
	case "budget_cycle_seconds":
		if val == "" {
			return a.unsetSetting("budget_cycle_seconds")
		}
		n, err := strconv.Atoi(strings.TrimSuffix(val, "s"))
		if err != nil || n <= 0 {
			return fmt.Errorf("budget cycle must be a positive number of seconds")
		}
		// Stored as typed; BudgetCycle clamps below the minimum when read.
		return a.mutateSetting(func(s *config.Settings) {
			if s.UI == nil {
				s.UI = &config.UISettings{}
			}
			s.UI.BudgetCycleSeconds = &n
		})
	case "tests.command":
		if val == "" {
			return a.unsetSetting("tests.command")
		}
		argv := strings.Fields(val)
		if len(argv) == 0 {
			return fmt.Errorf("test command must name a command")
		}
		return a.mutateGlobalSetting(func(s *config.Settings) {
			if s.Tests == nil {
				s.Tests = &config.TestsSettings{}
			}
			s.Tests.Command = argv
		})
	case "tests.max_fix_passes":
		if val == "" {
			return a.unsetSetting("tests.max_fix_passes")
		}
		n, err := strconv.Atoi(val)
		if err != nil || n <= 0 {
			return fmt.Errorf("test fix passes must be a positive integer")
		}
		return a.mutateGlobalSetting(func(s *config.Settings) {
			if s.Tests == nil {
				s.Tests = &config.TestsSettings{}
			}
			s.Tests.MaxFixPasses = n
		})
	case "tests.timeout_seconds":
		if val == "" {
			return a.unsetSetting("tests.timeout_seconds")
		}
		n, err := strconv.Atoi(strings.TrimSuffix(val, "s"))
		if err != nil || n <= 0 {
			return fmt.Errorf("test timeout must be a positive number of seconds")
		}
		return a.mutateGlobalSetting(func(s *config.Settings) {
			if s.Tests == nil {
				s.Tests = &config.TestsSettings{}
			}
			s.Tests.TimeoutSeconds = n
		})
	}
	return fmt.Errorf("cannot edit %q", row.key)
}

func (a *App) cycleToggle(key string) error {
	if job, ok := strings.CutPrefix(key, jevRowPrefix); ok && config.ValidJevJob(job) {
		on := !a.settings.JevJobSet(config.JevJob(job))
		return a.setJevJob(config.JevJob(job), &on)
	}
	if key == "auto_commit_per_task" {
		// This toggle is global only: a repo-visible settings file must never
		// be able to make the harness commit on the user's behalf.
		return a.mutateGlobalSetting(func(s *config.Settings) {
			s.AutoCommitPerTask = nextBool(s.AutoCommitPerTask)
		})
	}
	if key == "tests.report" {
		// The report is an extra model call, so it is opt-in and global only,
		// like the rest of the tests block's user-facing keys.
		return a.mutateGlobalSetting(func(s *config.Settings) {
			if s.Tests == nil {
				s.Tests = &config.TestsSettings{}
			}
			// The report defaults on, so the cycle is on (unset) -> off -> on.
			if s.Tests.Report == nil || *s.Tests.Report {
				f := false
				s.Tests.Report = &f
			} else {
				s.Tests.Report = nil
			}
		})
	}
	if strings.HasPrefix(key, "voice.") {
		return a.voiceToggle(key)
	}
	if key == "plan_limits" {
		// Global only: a repository must not change what a user learns about
		// their own account limits.
		return a.mutateGlobalSetting(func(s *config.Settings) {
			if s.Intel == nil {
				s.Intel = &config.IntelSettings{}
			}
			s.Intel.PlanLimits = nextBool(s.Intel.PlanLimits)
		})
	}
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
		case "budget_warn":
			if s.UI == nil {
				s.UI = &config.UISettings{}
			}
			s.UI.BudgetWarn = nextBool(s.UI.BudgetWarn)
		case "intel":
			if s.UI == nil {
				s.UI = &config.UISettings{}
			}
			s.UI.Intel = nextBool(s.UI.Intel)
		}
	})
}

func (a *App) cycleChoice(key string, opts []string) error {
	if strings.HasPrefix(key, "voice.") {
		return a.voiceChoose(key, opts)
	}
	// The tests block runs commands, so its rows always write the global
	// scope; every other choice follows the scope the user selected.
	mutate := a.mutateSetting
	if strings.HasPrefix(key, "tests.") {
		mutate = a.mutateGlobalSetting
	}
	return mutate(func(s *config.Settings) {
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
		case "tests.post_end":
			if s.Tests == nil {
				s.Tests = &config.TestsSettings{}
			}
			idx := indexOfString(opts, s.Tests.PostEnd)
			s.Tests.PostEnd = opts[(idx+1)%len(opts)]
		case "tests.scope":
			if s.Tests == nil {
				s.Tests = &config.TestsSettings{}
			}
			idx := indexOfString(opts, s.Tests.Scope)
			s.Tests.Scope = opts[(idx+1)%len(opts)]
		case "tests.on_fail":
			if s.Tests == nil {
				s.Tests = &config.TestsSettings{}
			}
			idx := indexOfString(opts, s.Tests.OnFail)
			s.Tests.OnFail = opts[(idx+1)%len(opts)]
		}
	})
}

func (a *App) unsetSetting(key string) error {
	if job, ok := strings.CutPrefix(key, jevRowPrefix); ok && config.ValidJevJob(job) {
		return a.setJevJob(config.JevJob(job), nil)
	}
	if key == "auto_commit_per_task" {
		return a.mutateGlobalSetting(func(s *config.Settings) {
			s.AutoCommitPerTask = nil
		})
	}
	if strings.HasPrefix(key, "tests.") {
		return a.unsetTestsKey(key)
	}
	if strings.HasPrefix(key, "voice.") {
		return a.voiceUnset(key)
	}
	if key == "plan_limits" {
		return a.mutateGlobalSetting(func(s *config.Settings) {
			if s.Intel != nil {
				s.Intel.PlanLimits = nil
			}
		})
	}
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
		case "budget_cycle_seconds":
			if s.UI != nil {
				s.UI.BudgetCycleSeconds = nil
			}
		case "budget_warn":
			if s.UI != nil {
				s.UI.BudgetWarn = nil
			}
		case "intel":
			if s.UI != nil {
				s.UI.Intel = nil
			}
		}
	})
}

// unsetTestsKey clears one tests.* setting in the global layer. The whole
// tests block is global-only for every key except the off/tighten project
// exemptions.
func (a *App) unsetTestsKey(key string) error {
	return a.mutateGlobalSetting(func(s *config.Settings) {
		if s.Tests == nil {
			return
		}
		switch key {
		case "tests.post_end":
			s.Tests.PostEnd = ""
		case "tests.scope":
			s.Tests.Scope = ""
		case "tests.on_fail":
			s.Tests.OnFail = ""
		case "tests.command":
			s.Tests.Command = nil
		case "tests.report":
			s.Tests.Report = nil
		case "tests.max_fix_passes":
			s.Tests.MaxFixPasses = 0
		case "tests.timeout_seconds":
			s.Tests.TimeoutSeconds = 0
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

// mutateGlobalSetting writes one setting to the global settings file,
// regardless of the /settings scope row. It is used by settings that are
// security-relevant and must never be project-overridable.
func (a *App) mutateGlobalSetting(fn func(*config.Settings)) error {
	if err := config.Mutate(config.ScopeGlobal, a.workdir, func(s *config.Settings) error {
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
