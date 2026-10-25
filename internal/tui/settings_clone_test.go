package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/config"
)

// clone repositories (agents.clone) is on by default, sits under Agents, and
// is written to the global settings whatever scope the screen shows.
func TestCloneRepositoriesToggleIsGlobal(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})
	a.push(viewSettings)
	a.settingsState.scope = config.ScopeProject

	row, idx := settingsRowByKey(a, "agents.clone")
	if idx < 0 {
		t.Fatal("no agents.clone row")
	}
	if row.value != "on" || settingsGroupOf(row.key) != "agents" || row.help == "" {
		t.Fatalf("row = %+v in group %q", row, settingsGroupOf(row.key))
	}
	a.settingsState.selected = idx
	m, _ := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	a = m.(*App)
	global, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if global.AgentsCloneEnabled() {
		t.Fatalf("the toggle did not turn agents.clone off globally: %+v", global.Agents)
	}
	project, err := config.LoadProject(workdir)
	if err != nil {
		t.Fatal(err)
	}
	if project.Agents != nil && project.Agents.Clone != nil {
		t.Fatal("the toggle wrote the project layer")
	}
	if row, _ := settingsRowByKey(a, "agents.clone"); row.value != "off" {
		t.Fatalf("row after the toggle = %q", row.value)
	}
	if err := a.unsetSetting("agents.clone"); err != nil {
		t.Fatal(err)
	}
	if global, _ := config.LoadGlobal(); !global.AgentsCloneEnabled() {
		t.Fatal("unset did not return agents.clone to on")
	}
}
