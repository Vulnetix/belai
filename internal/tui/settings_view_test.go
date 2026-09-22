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
		"plan_explore":       "on",
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
