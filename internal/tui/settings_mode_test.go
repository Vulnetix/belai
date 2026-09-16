package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/posture"
)

func enforceNoMatchPostureForTest() posture.Policy {
	p := posture.Defaults()
	p[posture.PermissionNoMatch] = posture.Enforce
	return p
}

// ---------------------------------------------------------------------------
// /settings "read-only tools" toggle
// ---------------------------------------------------------------------------

func TestSettingsReadOnlyRowRendersOffByDefault(t *testing.T) {
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})
	a.push(viewSettings)

	v := a.View()
	if !strings.Contains(v, "read-only tools") {
		t.Fatalf("settings view missing read-only tools row:\n%s", v)
	}

	rows := a.settingsRows()
	idx := -1
	for i, r := range rows {
		if r.key == "read_only" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("no read_only row in settingsRows")
	}
	if rows[idx].value != "off" {
		t.Fatalf("read-only tools should render off by default, got %q", rows[idx].value)
	}
}

func TestSettingsReadOnlySpaceTogglesAndPersists(t *testing.T) {
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})
	a.push(viewSettings)

	rows := a.settingsRows()
	idx := -1
	for i, r := range rows {
		if r.key == "read_only" {
			idx = i
		}
	}
	a.settingsState.selected = idx

	// space: off (nil) -> on (true), persisted to the scoped settings file.
	m, _ := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	a = m.(*App)
	got, err := config.LoadProject(workdir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if got.ReadOnly == nil || !*got.ReadOnly {
		t.Fatalf("space should persist read_only=true to project settings, got %+v", got.ReadOnly)
	}

	// space again: on -> explicit off.
	m, _ = a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	a = m.(*App)
	got, err = config.LoadProject(workdir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if got.ReadOnly == nil || *got.ReadOnly {
		t.Fatalf("second toggle should persist read_only=false, got %+v", got.ReadOnly)
	}

	// x: unset (back to nil).
	a.settingsState.selected = idx
	m, _ = a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	a = m.(*App)
	got, err = config.LoadProject(workdir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if got.ReadOnly != nil {
		t.Fatalf("x should unset read_only, got %+v", got.ReadOnly)
	}
}

func TestSettingsReadOnlyGlobalScope(t *testing.T) {
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})
	a.push(viewSettings)
	a.settingsState.scope = config.ScopeGlobal

	rows := a.settingsRows()
	for i, r := range rows {
		if r.key == "read_only" {
			a.settingsState.selected = i
		}
	}
	m, _ := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	a = m.(*App)

	got, err := config.LoadGlobal()
	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}
	if got.ReadOnly == nil || !*got.ReadOnly {
		t.Fatalf("global scope should persist read_only=true, got %+v", got.ReadOnly)
	}
	if p, err := config.LoadProject(workdir); err == nil && p.ReadOnly != nil {
		t.Fatalf("global-scope toggle must not touch project settings, got %+v", p.ReadOnly)
	}
}

// ---------------------------------------------------------------------------
// Mode chip drives the agent session's plan mode
// ---------------------------------------------------------------------------

func TestNewInitialisesPlanModeFromRestoredMode(t *testing.T) {
	workdir := t.TempDir()
	st, _ := config.LoadState()
	st.LastMode = "plan"
	if err := config.SaveState(st); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	t.Cleanup(func() {
		st2, _ := config.LoadState()
		st2.LastMode = ""
		_ = config.SaveState(st2)
	})

	a := New(Options{Workdir: workdir})
	if a.mode != "plan" {
		t.Fatalf("mode = %q, want plan (restored from state)", a.mode)
	}
	if !a.planMode {
		t.Fatalf("restored plan mode should initialise planMode=true")
	}
}

func TestModeChipDrivesAgentPlanMode(t *testing.T) {
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})

	// Seed a cached agent session so invalidation is observable.
	if _, err := a.agentSession(); err != nil {
		t.Fatalf("agentSession: %v", err)
	}
	if a.agent == nil {
		t.Fatalf("expected a cached agent session")
	}

	// agent -> plan: planMode flips and the cached session is dropped so the
	// next agent.NewSession receives PlanMode=true.
	a.cycleMode()
	if a.mode != "plan" || !a.planMode {
		t.Fatalf("after cycle: mode=%q planMode=%v, want plan/true", a.mode, a.planMode)
	}
	if a.agent != nil {
		t.Fatalf("cycling mode must invalidate the cached agent session")
	}
	sess, err := a.agentSession()
	if err != nil {
		t.Fatalf("agentSession: %v", err)
	}
	if !sess.PlanMode() {
		t.Fatalf("next agent session should receive PlanMode=true")
	}

	// plan -> goal: full tools restored.
	a.cycleMode()
	sess2, err := a.agentSession()
	if err != nil {
		t.Fatalf("agentSession: %v", err)
	}
	if a.planMode || sess2.PlanMode() {
		t.Fatalf("goal mode should run with full tools, planMode=%v", a.planMode)
	}
}

func TestSlashModeSetsPlanMode(t *testing.T) {
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})
	if _, err := a.agentSession(); err != nil {
		t.Fatalf("agentSession: %v", err)
	}

	a.handleCommand("/mode plan")
	if a.mode != "plan" || !a.planMode {
		t.Fatalf("/mode plan should enable plan mode: mode=%q planMode=%v", a.mode, a.planMode)
	}
	if a.agent != nil {
		t.Fatalf("/mode plan must invalidate the cached agent session")
	}
	sess, err := a.agentSession()
	if err != nil {
		t.Fatalf("agentSession: %v", err)
	}
	if !sess.PlanMode() {
		t.Fatalf("next agent session should receive PlanMode=true")
	}

	a.handleCommand("/mode agent")
	if a.mode != "agent" || a.planMode {
		t.Fatalf("/mode agent should restore agent mode: mode=%q planMode=%v", a.mode, a.planMode)
	}
}

func TestClassifyModeSyncsPlanMode(t *testing.T) {
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})
	a.SetClassifier(&fakeClassifier{raw: "PLAN"})

	a.classifyMode("figure out how to refactor this")

	if a.mode != "plan" {
		t.Fatalf("mode = %q, want plan", a.mode)
	}
	if !a.planMode {
		t.Fatalf("classifier-selected plan mode should enable planMode")
	}
}

// ---------------------------------------------------------------------------
// /permissions copy reflects the default-allow semantics
// ---------------------------------------------------------------------------

func TestPermissionsViewEmptyStateAllows(t *testing.T) {
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})
	a.push(viewSettings)
	a.push(viewPermissions)

	v := a.View()
	if !strings.Contains(v, "no rules — every tool call is allowed") {
		t.Fatalf("empty permissions view should say every tool call is allowed:\n%s", v)
	}
	if strings.Contains(v, "every tool call is denied") {
		t.Fatalf("stale denied wording still present:\n%s", v)
	}
	if strings.Contains(v, "the TUI has no tool loop") {
		t.Fatalf("stale no-tool-loop footer still present:\n%s", v)
	}
}

func TestPermissionsPreviewDefaultAllowRespectsPosture(t *testing.T) {
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})
	a.push(viewPermissions)
	a.permState.mode = "preview"
	a.permState.previewSubject = "Read notes.txt"

	v := a.View()
	if !strings.Contains(v, "allowed (no rule matches") {
		t.Fatalf("default posture should preview allow for unmatched calls:\n%s", v)
	}

	// permission_no_match: enforce restores the legacy block wording.
	a.posture = enforceNoMatchPostureForTest()
	v = a.View()
	if !strings.Contains(v, "blocked (no rule matches") {
		t.Fatalf("enforce posture should preview block for unmatched calls:\n%s", v)
	}
}
