package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/belai/internal/config"
)

func settingsGroupedApp(t *testing.T, w, h int) *App {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	a.width, a.height = w, h
	a.push(viewSettings)
	return a
}

func TestEverySettingsRowHasANamedGroup(t *testing.T) {
	a := jevThresholdApp(t) // with a decision backend, so every group exists
	known := map[string]bool{}
	for _, d := range settingsGroupDefs {
		known[d.key] = true
	}
	seen := map[string]bool{}
	for _, r := range a.settingsRows() {
		if !known[r.group] {
			t.Fatalf("row %q has group %q, which is not on the rail", r.key, r.group)
		}
		if seen[r.key] {
			t.Fatalf("row %q appears twice", r.key)
		}
		seen[r.key] = true
	}
	total := 0
	for _, g := range a.settingsGroups(a.settingsRows()) {
		total += g.selectable()
	}
	if total != len(seen) {
		t.Fatalf("the rail lists %d rows, the screen has %d", total, len(seen))
	}
}

func TestEveryThresholdIsInExactlyOneSection(t *testing.T) {
	seen := map[string]int{}
	for _, sec := range jevThresholdSections {
		for _, k := range sec.keys {
			seen[k]++
		}
	}
	for _, k := range config.JevThresholdKeys() {
		if seen[k] != 1 {
			t.Fatalf("threshold %q is in %d sections", k, seen[k])
		}
	}
}

func TestSettingsViewFitsATerminal(t *testing.T) {
	for _, sz := range [][2]int{{80, 24}, {100, 30}, {120, 40}, {60, 20}} {
		a := jevThresholdApp(t)
		a.width, a.height = sz[0], sz[1]
		for _, g := range a.settingsGroups(a.settingsRows()) {
			a.settingsState.selected = glFirst(g)
			out := a.View()
			lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
			if len(lines) > sz[1] {
				t.Fatalf("%dx%d group %q: %d lines\n%s", sz[0], sz[1], g.title, len(lines), out)
			}
			for _, l := range lines {
				if ansi.StringWidth(l) > sz[0] {
					t.Fatalf("%dx%d group %q: line %d cells wide: %q", sz[0], sz[1], g.title, ansi.StringWidth(l), l)
				}
			}
		}
	}
}

func TestSettingsBracketKeysSwitchGroup(t *testing.T) {
	a := settingsGroupedApp(t, 120, 40)
	a = pressKey(a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{']'}})
	rows := a.settingsRows()
	if got := rows[a.settingsState.selected].group; got != "display" {
		t.Fatalf("] moved to group %q, want display", got)
	}
	a = pressKey(a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'['}})
	if got := rows[a.settingsState.selected].group; got != "general" {
		t.Fatalf("[ moved to group %q, want general", got)
	}
	a = pressKey(a, tea.KeyMsg{Type: tea.KeyRight})
	if got := rows[a.settingsState.selected].group; got != "display" {
		t.Fatalf("right off a slider should move to the next group, got %q", got)
	}
}

func TestSettingsDownStaysInsideTheGroup(t *testing.T) {
	a := settingsGroupedApp(t, 120, 40)
	_, last := settingsRowByKey(a, "auto_commit_per_task")
	a.settingsState.selected = last
	a = pressKey(a, tea.KeyMsg{Type: tea.KeyDown})
	if a.settingsState.selected != last {
		t.Fatalf("down past the group's last row moved to %d", a.settingsState.selected)
	}
}

func TestSettingsSliderArrowsStillNudge(t *testing.T) {
	a := jevThresholdApp(t)
	a.width, a.height = 120, 40
	selectSliderRow(t, a, "deny_at")
	a = pressKey(a, tea.KeyMsg{Type: tea.KeyLeft})
	row, _ := settingsRowByKey(a, jevThresholdRowPrefix+"deny_at")
	if !strings.HasSuffix(row.value, "0.85") {
		t.Fatalf("left on a slider must nudge, not change group: %+v", row)
	}
	if row.group != "jevthresholds" {
		t.Fatalf("group = %q", row.group)
	}
}

func TestSettingsDependentRowsDim(t *testing.T) {
	a := settingsGroupedApp(t, 120, 40)
	scope := a.settingsState.scope
	_ = scope
	row, _ := settingsRowByKey(a, "tests.scope")
	if !row.dim || row.dimWhy == "" {
		t.Fatalf("test scope should wait on test pass while it is off: %+v", row)
	}
	if pass, _ := settingsRowByKey(a, "tests.post_end"); pass.dim {
		t.Fatal("test pass is the switch itself and must not dim")
	}
	if err := config.Mutate(config.ScopeGlobal, a.workdir, func(s *config.Settings) error {
		s.Tests = &config.TestsSettings{PostEnd: "goal"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.reloadSettings(); err != nil {
		t.Fatal(err)
	}
	if row, _ := settingsRowByKey(a, "tests.scope"); row.dim {
		t.Fatal("test scope should read normally once the pass is on")
	}
}

func TestSettingsSourceShownOnlyWhenNotDefault(t *testing.T) {
	a := settingsGroupedApp(t, 120, 40)
	out := a.View()
	if strings.Contains(out, "default ") && strings.Count(out, "default") > 3 {
		t.Fatalf("the source column should stay blank for defaults:\n%s", out)
	}
}

func TestSettingsFilterFindsAcrossGroups(t *testing.T) {
	a := settingsGroupedApp(t, 120, 40)
	a = pressKey(a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	for _, r := range "wake" {
		a = pressKey(a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	out := a.View()
	if !strings.Contains(out, "wake word") || strings.Contains(out, "provider") {
		t.Fatalf("filter should list only matching rows:\n%s", out)
	}
	a = pressKey(a, tea.KeyMsg{Type: tea.KeyEsc})
	if a.settingsState.gl.active() {
		t.Fatal("esc should clear the filter")
	}
	if a.view != viewSettings {
		t.Fatal("the first esc closes the filter, not the screen")
	}
}

func TestSettingsDetailNamesWhereItSaves(t *testing.T) {
	a := settingsGroupedApp(t, 120, 40)
	row, _ := settingsRowByKey(a, "auto_commit_per_task")
	if d := strings.Join(a.settingsDetail(row), "\n"); !strings.Contains(d, "global only") {
		t.Fatalf("auto-commit is global only: %s", d)
	}
	row, _ = settingsRowByKey(a, "max_agents")
	if d := strings.Join(a.settingsDetail(row), "\n"); !strings.Contains(d, "saved to project") {
		t.Fatalf("max agents follows the scope: %s", d)
	}
}
