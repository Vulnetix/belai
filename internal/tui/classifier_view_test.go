package tui

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/wire"
)

// newClassifierApp returns an App on the /classifier page with a custom
// provider carrying known model ids, writing into a temp SIGNET_HOME and a
// temp workdir so no real settings are touched.
func newClassifierApp(t *testing.T, cls *config.ClassifierSettings) *App {
	t.Helper()
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()

	if err := config.SaveGlobal(config.Settings{
		Provider: "my-llm",
		Model:    "big",
		Providers: map[string]config.ProviderProfile{
			"my-llm": {BaseURL: "https://llm.example/v1", API: wire.SurfaceOpenAIChat, Models: []config.ProviderModel{
				{ID: "big"}, {ID: "small"}, {ID: "tiny"},
			}},
		},
	}); err != nil {
		t.Fatalf("SaveGlobal: %v", err)
	}
	if cls != nil {
		if err := config.SaveProject(workdir, config.Settings{Classifier: cls}); err != nil {
			t.Fatalf("SaveProject: %v", err)
		}
	}

	a := New(Options{Workdir: workdir})
	a.classifierState = classifierViewState{scope: config.ScopeProject, lastEffort: "medium"}
	return a
}

// rowByKey returns the classifier row with the given key.
func rowByKey(t *testing.T, a *App, key string) settingsRow {
	t.Helper()
	for _, row := range a.classifierRows() {
		if row.key == key {
			return row
		}
	}
	t.Fatalf("no classifier row %q", key)
	return settingsRow{}
}

// selectRow points the cursor at the named row.
func selectRow(t *testing.T, a *App, key string) {
	t.Helper()
	for i, row := range a.classifierRows() {
		if row.key == key {
			a.classifierState.selected = i
			return
		}
	}
	t.Fatalf("no classifier row %q", key)
}

func key(s string) tea.KeyMsg {
	if s == " " {
		return tea.KeyMsg{Type: tea.KeySpace}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestClassifierRowsReflectSettings(t *testing.T) {
	cases := []struct {
		name         string
		cls          *config.ClassifierSettings
		wantProvider string
		wantModel    string
		wantReason   string
		wantEffort   string
		effortOff    bool
		wantCaveman  string
	}{
		{
			name: "unset inherits the main model",
			cls:  nil, wantProvider: "— (main: my-llm)", wantModel: "— (main: big)",
			wantReason: "off", wantEffort: "none (reasoning off)", effortOff: true, wantCaveman: "off",
		},
		{
			name:         "explicit model, reasoning off",
			cls:          &config.ClassifierSettings{Provider: "my-llm", Model: "small", Effort: "none"},
			wantProvider: "my-llm", wantModel: "small",
			wantReason: "off", wantEffort: "none (reasoning off)", effortOff: true, wantCaveman: "off",
		},
		{
			name:         "reasoning on carries the effort",
			cls:          &config.ClassifierSettings{Model: "tiny", Effort: "high", Caveman: boolPtr(true)},
			wantProvider: "— (main: my-llm)", wantModel: "tiny",
			wantReason: "on", wantEffort: "high", effortOff: false, wantCaveman: "on",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newClassifierApp(t, tc.cls)
			if got := rowByKey(t, a, "provider").value; got != tc.wantProvider {
				t.Fatalf("provider = %q, want %q", got, tc.wantProvider)
			}
			if got := rowByKey(t, a, "model").value; got != tc.wantModel {
				t.Fatalf("model = %q, want %q", got, tc.wantModel)
			}
			if got := rowByKey(t, a, "reasoning").value; got != tc.wantReason {
				t.Fatalf("reasoning = %q, want %q", got, tc.wantReason)
			}
			effort := rowByKey(t, a, "effort")
			if effort.value != tc.wantEffort {
				t.Fatalf("effort = %q, want %q", effort.value, tc.wantEffort)
			}
			if effort.disabled != tc.effortOff {
				t.Fatalf("effort disabled = %v, want %v", effort.disabled, tc.effortOff)
			}
			if got := rowByKey(t, a, "caveman").value; !strings.HasPrefix(got, tc.wantCaveman) {
				t.Fatalf("caveman = %q, want prefix %q", got, tc.wantCaveman)
			}
		})
	}
}

// TestReasoningToggleDrivesEffort pins the coupling: there is no separate
// reasoning key, so the toggle writes the effort and restores the remembered
// chip rather than the head of the list.
func TestReasoningToggleDrivesEffort(t *testing.T) {
	a := newClassifierApp(t, &config.ClassifierSettings{Effort: "high"})
	selectRow(t, a, "reasoning")

	a.handleClassifierKey(key(" "))
	if got := a.settings.Classifier.Effort; got != "none" {
		t.Fatalf("effort after toggling off = %q, want none", got)
	}
	if !rowByKey(t, a, "effort").disabled {
		t.Fatal("effort row must be disabled while reasoning is off")
	}

	a.handleClassifierKey(key(" "))
	if got := a.settings.Classifier.Effort; got != "high" {
		t.Fatalf("effort after toggling back on = %q, want the remembered high", got)
	}
	if rowByKey(t, a, "effort").disabled {
		t.Fatal("effort row must be live while reasoning is on")
	}
}

// TestEffortRowIgnoredWhileDisabled proves a greyed row is inert, not just
// grey: pressing space on it must not write the file.
func TestEffortRowIgnoredWhileDisabled(t *testing.T) {
	a := newClassifierApp(t, &config.ClassifierSettings{Effort: "none"})
	selectRow(t, a, "effort")

	path := config.ProjectSettingsPath(a.workdir)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	a.handleClassifierKey(key(" "))
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("disabled row wrote the file:\nbefore %s\nafter  %s", before, after)
	}
}

func TestClassifierCavemanToggleWritesSetting(t *testing.T) {
	a := newClassifierApp(t, nil)
	selectRow(t, a, "caveman")

	a.handleClassifierKey(key(" "))
	if !a.settings.ClassifierCavemanEnabled() {
		t.Fatal("toggling caveman on did not take")
	}
	got, err := config.LoadProject(a.workdir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if got.Classifier == nil || got.Classifier.Caveman == nil || !*got.Classifier.Caveman {
		t.Fatalf("caveman not persisted: %+v", got.Classifier)
	}

	a.handleClassifierKey(key(" "))
	if a.settings.ClassifierCavemanEnabled() {
		t.Fatal("toggling caveman off did not take")
	}
}

func TestClassifierScopeWritesChosenFile(t *testing.T) {
	a := newClassifierApp(t, nil)
	selectRow(t, a, "caveman")

	// s flips project -> global; the write must land in the global file only.
	a.handleClassifierKey(key("s"))
	if a.classifierState.scope != config.ScopeGlobal {
		t.Fatalf("scope = %q, want global", a.classifierState.scope)
	}
	a.handleClassifierKey(key(" "))

	global, err := config.LoadGlobal()
	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}
	if global.Classifier == nil || global.Classifier.Caveman == nil {
		t.Fatalf("global scope did not receive the write: %+v", global.Classifier)
	}
	proj, err := config.LoadProject(a.workdir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if proj.Classifier != nil && proj.Classifier.Caveman != nil {
		t.Fatalf("global write leaked into the project file: %+v", proj.Classifier)
	}
}

func TestClassifierPageListsOnlyAvailableProviders(t *testing.T) {
	a := newClassifierApp(t, nil)
	a.avail = providerAvailability{
		configured: map[string]bool{"openai": true, "ollama": true},
		local:      map[string]bool{"ollama": false},
		probedAt:   time.Now(),
	}
	// Without a resolver the filter degrades open, so give the page one.
	a.resolver = newTestResolver(t, a.workdir)

	opts := rowByKey(t, a, "provider").opts
	want := map[string]bool{"openai": true, "my-llm": true} // my-llm is the committed provider
	if len(opts) != len(want) {
		t.Fatalf("provider opts = %v, want exactly %v", opts, want)
	}
	for _, name := range opts {
		if !want[name] {
			t.Fatalf("provider opts offered %q, want only %v", name, want)
		}
	}
}

// TestClassifierUnsetClearsBlock pins the documented fallback: clearing every
// row leaves no classifier block, so the classifier follows the main model.
func TestClassifierUnsetClearsBlock(t *testing.T) {
	a := newClassifierApp(t, &config.ClassifierSettings{
		Provider: "my-llm", Model: "small", Effort: "high", Caveman: boolPtr(true),
	})
	for _, k := range []string{"provider", "model", "reasoning", "caveman"} {
		selectRow(t, a, k)
		a.handleClassifierKey(key("x"))
	}
	got, err := config.LoadProject(a.workdir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if !got.Classifier.IsZero() {
		t.Fatalf("classifier block survived unset: %+v", got.Classifier)
	}
	if a.classifierProvider() != a.cfg.Provider || a.classifierModel() != a.cfg.Model {
		t.Fatalf("cleared classifier does not follow the main model: %q/%q vs %q/%q",
			a.classifierProvider(), a.classifierModel(), a.cfg.Provider, a.cfg.Model)
	}
}

// TestClassifierProviderChangeClearsModel pins that a model id never survives
// a provider change: an id is only meaningful to its own provider. Cycling far
// enough wraps to "unset", which drops the block entirely — also a cleared
// model, and the documented fallback to the main model.
func TestClassifierProviderChangeClearsModel(t *testing.T) {
	a := newClassifierApp(t, &config.ClassifierSettings{Provider: "my-llm", Model: "small"})
	selectRow(t, a, "provider")

	for i := range a.classifierProviders() {
		a.handleClassifierKey(key(" "))
		cls := a.settings.Classifier
		if cls != nil && cls.Model != "" {
			t.Fatalf("step %d: model = %q after a provider change, want cleared", i, cls.Model)
		}
		if a.classifierModel() != a.cfg.Model {
			t.Fatalf("step %d: classifier model = %q, want the main model %q",
				i, a.classifierModel(), a.cfg.Model)
		}
	}
}

func TestClassifierModelPickerSetsModel(t *testing.T) {
	a := newClassifierApp(t, nil)
	selectRow(t, a, "model")

	a.handleClassifierKey(key(" "))
	if !a.classifierState.picking {
		t.Fatal("space on the model row must open the picker")
	}
	a.handleClassifierKey(tea.KeyMsg{Type: tea.KeyDown})
	a.handleClassifierKey(tea.KeyMsg{Type: tea.KeyEnter})

	if a.classifierState.picking {
		t.Fatal("enter must close the picker")
	}
	if got := a.settings.Classifier.Model; got != "small" {
		t.Fatalf("model = %q, want small", got)
	}
}

func TestClassifierPickerFilterTypesIntoTheBox(t *testing.T) {
	a := newClassifierApp(t, nil)
	selectRow(t, a, "model")
	a.handleClassifierKey(key(" "))

	a.handleClassifierKey(key("/"))
	for _, r := range "tin" {
		a.handleClassifierKey(key(string(r)))
	}
	if a.classifierState.filter != "tin" {
		t.Fatalf("filter = %q, want tin", a.classifierState.filter)
	}
	_, catalog := a.classifierCatalog()
	if len(catalog) != 1 || catalog[0].ID != "tiny" {
		t.Fatalf("filtered catalogue = %+v, want only tiny", catalog)
	}

	a.handleClassifierKey(tea.KeyMsg{Type: tea.KeyBackspace})
	if a.classifierState.filter != "ti" {
		t.Fatalf("filter after backspace = %q, want ti", a.classifierState.filter)
	}
}

func TestClassifierViewRendersWithoutPanic(t *testing.T) {
	widths := []int{0, 20, 100}
	for _, w := range widths {
		a := newClassifierApp(t, &config.ClassifierSettings{Model: "small", Effort: "high"})
		a.width, a.height = w, 24
		if out := a.classifierView(); out == "" {
			t.Fatalf("width %d rendered nothing", w)
		}
		a.classifierState.picking = true
		if out := a.classifierView(); out == "" {
			t.Fatalf("width %d rendered no picker", w)
		}
	}
}

// TestClassifierViewWarnsAboutWeakerModels keeps the security note on the page
// that chooses the security gate's model.
func TestClassifierViewWarnsAboutWeakerModels(t *testing.T) {
	a := newClassifierApp(t, nil)
	a.width, a.height = 120, 40
	if out := a.classifierView(); !strings.Contains(out, "security gate") {
		t.Fatalf("classifier page lost its warning:\n%s", out)
	}
}

// boolPtr is the tri-state helper the *bool settings need.
func boolPtr(b bool) *bool { return &b }
