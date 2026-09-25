package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/config"
)

func settingsRowByKey(a *App, key string) (settingsRow, int) {
	for i, r := range a.settingsRows() {
		if r.key == key {
			return r, i
		}
	}
	return settingsRow{}, -1
}

func TestInternalWorkRowCyclesAllLevelsAndUnsets(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})
	a.push(viewSettings)
	a.settingsState.scope = config.ScopeProject

	_, idx := settingsRowByKey(a, "show_internal_work")
	if idx < 0 {
		t.Fatal("no show_internal_work row")
	}
	a.settingsState.selected = idx

	for _, want := range []string{"decisions", "security", "all", "hidden"} {
		m, _ := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
		a = m.(*App)
		got, err := config.LoadProject(workdir)
		if err != nil {
			t.Fatalf("LoadProject: %v", err)
		}
		if got.UI == nil || got.UI.ShowInternalWork == nil || *got.UI.ShowInternalWork != want {
			t.Fatalf("after space: ShowInternalWork = %+v, want %q", got.UI, want)
		}
	}

	// x unsets the row back to nil.
	m, _ := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	a = m.(*App)
	got, err := config.LoadProject(workdir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if got.UI != nil && got.UI.ShowInternalWork != nil {
		t.Fatalf("x should unset show_internal_work, got %+v", got.UI.ShowInternalWork)
	}
}

func TestSettingsLSPSubmenuDispatches(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	a.push(viewSettings)
	row, idx := settingsRowByKey(a, "lsp")
	if idx < 0 {
		t.Fatal("no lsp row")
	}
	if row.kind != "submenu" {
		t.Fatalf("lsp row kind = %q, want submenu", row.kind)
	}
	a.settingsState.selected = idx
	m, _ := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	a = m.(*App)
	if a.view != viewLSP {
		t.Fatalf("space on lsp row should push viewLSP, got %v", a.view)
	}
}

func TestDisplayRowsShownHiddenAndBehaviourRowsOnOff(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	a.push(viewSettings)

	want := map[string]string{
		"banner":             "shown",
		"spinner":            "shown",
		"show_reasoning":     "hidden",
		"show_tool_calls":    "shown",
		"show_edits":         "shown",
		"show_todos":         "shown",
		"show_session_names": "shown",
		"read_only":          "off",
		"colors":             "on",
		"mouse":              "on",
		"update_check":       "on",
		"plan_explore":       "off",
		"goal_explore":       "off",
	}
	for key, want := range want {
		row, idx := settingsRowByKey(a, key)
		if idx < 0 {
			t.Fatalf("no row %q", key)
		}
		if row.value != want {
			t.Fatalf("row %q = %q, want %q", key, row.value, want)
		}
	}
}

func TestSettingsViewInternalWorkRowRenders(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	a.push(viewSettings)
	if !strings.Contains(a.View(), "internal work") {
		t.Fatalf("settings view missing internal work row:\n%s", a.View())
	}
}

func TestSettingsMaxAgentsPersistsAndReloads(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})
	a.push(viewSettings)
	a.settingsState.scope = config.ScopeProject

	row, idx := settingsRowByKey(a, "max_agents")
	if idx < 0 {
		t.Fatal("no max_agents row")
	}
	if row.value != "15" {
		t.Fatalf("default max_agents row = %q, want 15", row.value)
	}

	a.settingsState.selected = idx
	a.settingsState.editMode = true
	a.editor.SetValue("12")

	m, _ := a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	a = m.(*App)

	persisted, err := config.LoadProject(workdir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if persisted.Resilience == nil || persisted.Resilience.MaxAgents != 12 {
		t.Fatalf("project max_agents not persisted: %+v", persisted.Resilience)
	}
	if a.settings.Resilience == nil || a.settings.Resilience.MaxAgents != 12 {
		t.Fatalf("effective max_agents not reloaded: %+v", a.settings.Resilience)
	}
	if row, _ := settingsRowByKey(a, "max_agents"); row.value != "12" {
		t.Fatalf("max_agents row after edit = %q, want 12", row.value)
	}
}

// TestSettingsMaxAgentsResizesLivePool: the edit must reach the running
// session's fan-out ceiling, not only the settings file.
func TestSettingsMaxAgentsResizesLivePool(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	if got := a.agentPool.Size(); got != config.DefaultMaxAgents {
		t.Fatalf("initial pool size = %d, want %d", got, config.DefaultMaxAgents)
	}
	a.push(viewSettings)
	a.settingsState.scope = config.ScopeProject
	_, idx := settingsRowByKey(a, "max_agents")
	a.settingsState.selected = idx
	a.settingsState.editMode = true
	a.editor.SetValue("7")
	m, _ := a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	a = m.(*App)
	if got := a.agentPool.Size(); got != 7 {
		t.Fatalf("pool size after edit = %d, want 7", got)
	}
}

// TestSettingsGlobalEditShadowedByProjectExplains is the "I changed it and
// it never stuck" report: a global edit under a project override persists,
// and the screen says which layer wins instead of silently showing the old
// value.
func TestSettingsGlobalEditShadowedByProjectExplains(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()
	if err := config.Mutate(config.ScopeProject, workdir, func(s *config.Settings) error {
		s.Resilience = &config.ResilienceSettings{MaxAgents: 30}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	a := New(Options{Workdir: workdir})
	a.push(viewSettings)
	m, _ := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	a = m.(*App)
	if a.settingsState.scope != config.ScopeGlobal {
		t.Fatalf("scope = %v, want global after s", a.settingsState.scope)
	}
	_, idx := settingsRowByKey(a, "max_agents")
	a.settingsState.selected = idx
	a.settingsState.editMode = true
	a.editor.SetValue("20")
	m, _ = a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	a = m.(*App)

	global, err := config.LoadGlobal()
	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}
	if global.Resilience == nil || global.Resilience.MaxAgents != 20 {
		t.Fatalf("global max_agents not persisted: %+v", global.Resilience)
	}
	if !strings.Contains(a.settingsState.notice, "project wins") {
		t.Fatalf("notice = %q, want it to name the winning project layer", a.settingsState.notice)
	}

	// Reopening /settings keeps the scope the user chose.
	a.pop()
	a.handleCommand("/settings")
	if a.settingsState.scope != config.ScopeGlobal {
		t.Fatalf("reopened scope = %v, want the chosen global scope", a.settingsState.scope)
	}
}

// TestReadOnlyNoticeNamesSourceAndScope: the read_only setting must never be
// a silent reason agent mode cannot write.
func TestReadOnlyNoticeNamesSourceAndScope(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()
	on := true
	if err := config.Mutate(config.ScopeProject, workdir, func(s *config.Settings) error {
		s.ReadOnly = &on
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	a := New(Options{Workdir: workdir})
	note := a.readOnlyNotice()
	if !strings.Contains(note, "project") || !strings.Contains(note, "goal mode and approved plans are unaffected") {
		t.Fatalf("notice = %q", note)
	}

	a.planExecuting = true
	a.mode = "goal"
	a.cycleMode()
	if a.planExecuting {
		t.Fatal("changing mode must end plan execution")
	}
}
