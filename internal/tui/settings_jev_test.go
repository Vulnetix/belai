package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/config"
)

func jevRowKeys(a *App) []string {
	var keys []string
	for _, r := range a.settingsRows() {
		if strings.HasPrefix(r.key, jevRowPrefix) {
			keys = append(keys, r.key)
		}
	}
	return keys
}

// Without a decision backend the Jev jobs are off and hidden: no row, not a
// greyed one.
func TestJevRowsAreHiddenWithoutADecisionBackend(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	if keys := jevRowKeys(a); len(keys) != 0 {
		t.Fatalf("Jev rows shown with no decision backend: %v", keys)
	}
}

func TestJevRowsAppearWithADecisionBackendAndDefaultOn(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	a.settings.Classifier = &config.ClassifierSettings{Provider: "decision-local", Model: "decider-4b"}
	keys := jevRowKeys(a)
	if len(keys) != len(config.JevJobs) {
		t.Fatalf("rows = %v, want one per job (%d)", keys, len(config.JevJobs))
	}
	for i, j := range config.JevJobs {
		if keys[i] != jevRowPrefix+string(j) {
			t.Errorf("row %d = %q, want %q (documented order)", i, keys[i], jevRowPrefix+string(j))
		}
	}
	for _, r := range a.settingsRows() {
		if !strings.HasPrefix(r.key, jevRowPrefix) {
			continue
		}
		if r.value != "on" || r.kind != "toggle" || r.help == "" || r.label == "" {
			t.Errorf("row %+v: want a labelled, described toggle that defaults on", r)
		}
	}
}

func TestEveryJevJobHasALabelAndHelp(t *testing.T) {
	for _, j := range config.JevJobs {
		if jevJobLabels[j] == "" || jevJobHelp[j] == "" {
			t.Errorf("job %s has no /settings label or help", j)
		}
	}
	if len(jevJobLabels) != len(config.JevJobs) || len(jevJobHelp) != len(config.JevJobs) {
		t.Error("label or help table has an entry for a job that does not exist")
	}
}

func TestJevRowTogglesPersistsAndUnsets(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})
	a.push(viewSettings)
	a.settingsState.scope = config.ScopeGlobal
	// A decision backend must be configured for the row to exist; put it in the
	// same scope so it survives the reloads the toggles trigger.
	if err := config.Mutate(config.ScopeGlobal, workdir, func(s *config.Settings) error {
		s.Classifier = &config.ClassifierSettings{Provider: "decision-local", Model: "decider-4b"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.reloadSettings(); err != nil {
		t.Fatal(err)
	}

	key := jevRowPrefix + string(config.JevBashSwap)
	_, idx := settingsRowByKey(a, key)
	if idx < 0 {
		t.Fatal("no bash swap row")
	}
	a.settingsState.selected = idx

	m, _ := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	a = m.(*App)
	got, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if got.Jev == nil || got.Jev.Jobs["bash_swap"] {
		t.Fatalf("first toggle should switch the job off: %+v", got.Jev)
	}
	if a.settings.JevJobSet(config.JevBashSwap) {
		t.Fatal("live settings still show the job on")
	}
	if row, _ := settingsRowByKey(a, key); row.value != "off" {
		t.Fatalf("row value = %q, want off", row.value)
	}

	m, _ = a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	a = m.(*App)
	if got, _ = config.LoadGlobal(); got.Jev == nil || !got.Jev.Jobs["bash_swap"] {
		t.Fatalf("second toggle should switch it back on: %+v", got.Jev)
	}

	m, _ = a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	a = m.(*App)
	if got, _ = config.LoadGlobal(); got.Jev != nil {
		if _, set := got.Jev.Jobs["bash_swap"]; set {
			t.Fatalf("x should remove the explicit switch: %+v", got.Jev)
		}
	}
	if !a.settings.JevJobSet(config.JevBashSwap) {
		t.Fatal("an unset job should read on")
	}
}

func TestToggleDropsTheCachedSessionSoTheSwitchApplies(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})
	a.settingsState.scope = config.ScopeGlobal
	if err := a.setJevJob(config.JevBashSwap, boolPtr(false)); err != nil {
		t.Fatal(err)
	}
	if a.agent != nil {
		t.Fatal("the cached session survived a Jev switch")
	}
}
