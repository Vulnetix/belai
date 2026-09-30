package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/config"
)

func jevThresholdApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Cleanup(func() { config.SetActiveJevThresholds(config.DefaultJevThresholds()) })
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})
	a.push(viewSettings)
	a.settingsState.scope = config.ScopeGlobal
	if err := config.Mutate(config.ScopeGlobal, workdir, func(s *config.Settings) error {
		s.Classifier = &config.ClassifierSettings{Provider: "decision-local", Model: "decider-4b"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.reloadSettings(); err != nil {
		t.Fatal(err)
	}
	return a
}

func selectSliderRow(t *testing.T, a *App, key string) {
	t.Helper()
	_, idx := settingsRowByKey(a, jevThresholdRowPrefix+key)
	if idx < 0 {
		t.Fatalf("no %s slider row", key)
	}
	a.settingsState.selected = idx
}

func pressKey(a *App, k tea.KeyMsg) *App {
	m, _ := a.Update(k)
	return m.(*App)
}

func TestSliderBarMovesInEighthsOfACell(t *testing.T) {
	if got := []rune(sliderBar(0)); len(got) != sliderCells || strings.ContainsRune(string(got), '█') {
		t.Fatalf("empty bar = %q", string(got))
	}
	if got := sliderBar(1); strings.ContainsRune(got, '·') {
		t.Fatalf("full bar has track left: %q", got)
	}
	seen := map[string]bool{}
	for i := 0; i <= 100; i++ {
		seen[sliderBar(float64(i)/100)] = true
	}
	// 101 hundredths land on at least 90 distinct bars: a 0.01 step is visible.
	if len(seen) < 90 {
		t.Fatalf("only %d distinct bars over 101 values", len(seen))
	}
	for b := range seen {
		if n := len([]rune(b)); n != sliderCells {
			t.Fatalf("bar %q is %d cells wide, want %d", b, n, sliderCells)
		}
	}
}

func TestJevSlidersOnlyWithADecisionBackend(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	for _, r := range a.settingsRows() {
		if r.kind == "slider" {
			t.Fatalf("slider %s shown with no decision backend", r.key)
		}
	}
	a = jevThresholdApp(t)
	n := 0
	for _, r := range a.settingsRows() {
		if r.kind == "slider" {
			n++
			if r.label == "" || r.help == "" || r.src != "default" {
				t.Errorf("slider %+v: want a label, help and the default source", r)
			}
		}
	}
	if n != len(config.JevThresholdKeys()) {
		t.Fatalf("%d sliders, want %d", n, len(config.JevThresholdKeys()))
	}
	if len(jevThresholdLabels) != n || len(jevThresholdHelp) != n {
		t.Fatal("label or help table does not match the threshold keys")
	}
}

func TestSliderStepsSavesAndResets(t *testing.T) {
	a := jevThresholdApp(t)
	selectSliderRow(t, a, "deny_at")

	a = pressKey(a, tea.KeyMsg{Type: tea.KeyLeft})
	got, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if v := got.JevThresholds().DenyAt; v != 0.85 {
		t.Fatalf("left should step deny_at down by 0.05, got %v", v)
	}
	if v := config.ActiveJevThresholds().DenyAt; v != 0.85 {
		t.Fatalf("the change must take effect live, active deny_at = %v", v)
	}
	if row, _ := settingsRowByKey(a, jevThresholdRowPrefix+"deny_at"); !strings.HasSuffix(row.value, "0.85") || row.src == "default" {
		t.Fatalf("row = %+v", row)
	}

	a = pressKey(a, tea.KeyMsg{Type: tea.KeyShiftRight})
	if got, _ = config.LoadGlobal(); got.JevThresholds().DenyAt != 0.86 {
		t.Fatalf("shift+right should step 0.01, got %v", got.JevThresholds().DenyAt)
	}

	a = pressKey(a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if got, _ = config.LoadGlobal(); got.JevThresholds().DenyAt != 0.90 {
		t.Fatalf("x should restore the default, got %v", got.JevThresholds().DenyAt)
	}
	if row, _ := settingsRowByKey(a, jevThresholdRowPrefix+"deny_at"); row.src != "default" {
		t.Fatalf("source after reset = %q", row.src)
	}
}

func TestSliderRefusesAStepThatBreaksARule(t *testing.T) {
	a := jevThresholdApp(t)
	selectSliderRow(t, a, "keep_at")
	for i := 0; i < 6; i++ { // 0.50 to 0.80, the strong_at default
		a = pressKey(a, tea.KeyMsg{Type: tea.KeyRight})
	}
	if a.settingsState.errorMsg != "" {
		t.Fatalf("0.80 is allowed, got %q", a.settingsState.errorMsg)
	}
	a = pressKey(a, tea.KeyMsg{Type: tea.KeyRight}) // 0.85 would pass strong_at
	if !strings.Contains(a.settingsState.errorMsg, "keep_at") {
		t.Fatalf("want the rule's message, got %q", a.settingsState.errorMsg)
	}
	if got, _ := config.LoadGlobal(); got.JevThresholds().KeepAt != 0.80 {
		t.Fatalf("a refused step must write nothing, keep_at = %v", got.JevThresholds().KeepAt)
	}
}
