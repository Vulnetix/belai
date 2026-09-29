package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/modeltest"
)

// lineCount is the number of terminal rows a rendered view occupies.
func lineCount(view string) int { return strings.Count(strings.TrimRight(view, "\n"), "\n") + 1 }

// The roles screen has to fit the terminal it is drawn in: no row past the
// bottom, no cell past the right edge, and the key bar and the selected field
// always on screen, whatever role is selected.
func TestModelScreenFitsEveryTerminal(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	sizes := [][2]int{{80, 24}, {100, 24}, {120, 24}, {120, 30}, {140, 40}, {200, 60}, {90, 20}}
	for _, sz := range sizes {
		w, h := sz[0], sz[1]
		a := newModelScreen(t, t.TempDir())
		a.Update(tea.WindowSizeMsg{Width: w, Height: h})
		rows := a.modelRows()
		for i, r := range rows {
			a.modelState.selected = i
			view := a.modelView()
			if got := lineCount(view); got > h {
				t.Fatalf("%dx%d %s/%s: view is %d rows tall\n%s", w, h, r.role, r.key, got, ansi.Strip(view))
			}
			for _, line := range strings.Split(view, "\n") {
				if got := ansi.StringWidth(line); got > w {
					t.Fatalf("%dx%d %s/%s: line is %d cells: %q", w, h, r.role, r.key, got, ansi.Strip(line))
				}
			}
			plain := ansi.Strip(view)
			if !strings.Contains(plain, "esc back") {
				t.Fatalf("%dx%d %s/%s: key bar cut off\n%s", w, h, r.role, r.key, plain)
			}
			if !strings.Contains(plain, "▸") {
				t.Fatalf("%dx%d %s/%s: selected field not on screen\n%s", w, h, r.role, r.key, plain)
			}
		}
	}
}

// A failed test shows in a strip under the panes without pushing the panes or
// the keys off screen, and `d` opens the full log.
func TestModelStatusStripFitsAndLogOpens(t *testing.T) {
	a := flowScreen(t, failingTester("llama-server is not on PATH"))
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	_ = a.stageClassifier("kind", "classifier.kind = llm", func(c *config.ClassifierSettings) { c.Kind = "llm" })
	pump(t, a)
	selectRow(t, a, roleClassifier, "kind")

	view := a.modelView()
	if got := lineCount(view); got > 24 {
		t.Fatalf("view is %d rows tall\n%s", got, ansi.Strip(view))
	}
	plain := ansi.Strip(view)
	for _, want := range []string{"✗ not saved", "d test log", "esc back"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("view lacks %q\n%s", want, plain)
		}
	}

	_, _ = a.handleModelKey(modelKey("d"))
	if !a.modelState.showLog {
		t.Fatal("d did not open the test log")
	}
	log := ansi.Strip(a.modelView())
	if !strings.Contains(log, "Model test log") || !strings.Contains(log, "llama-server") {
		t.Fatalf("log view wrong:\n%s", log)
	}
	if got := lineCount(a.modelView()); got > 24 {
		t.Fatalf("log view is %d rows tall", got)
	}
	_, _ = a.handleModelKey(tea.KeyMsg{Type: tea.KeyEsc})
	if a.modelState.showLog {
		t.Fatal("esc did not close the test log")
	}
}

// The routing pool is the one list longer than a short terminal. It scrolls
// inside its pane, keeps the cursor row visible and says how much is hidden.
func TestModelRoutingPoolScrollsInsidePane(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	a := newModelScreen(t, t.TempDir())
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 20})

	selectRow(t, a, roleRouting, "kind")
	top := ansi.Strip(a.modelView())
	if !strings.Contains(top, "below") || strings.Contains(top, "web_fetch") {
		t.Fatalf("top of the pool should hide its tail:\n%s", top)
	}

	selectRow(t, a, roleRouting, "route:web_fetch")
	bottom := ansi.Strip(a.modelView())
	if !strings.Contains(bottom, "web_fetch") || !strings.Contains(bottom, "above") {
		t.Fatalf("end of the pool should show the last use case and what is above:\n%s", bottom)
	}
	if got := lineCount(bottom); got > 20 {
		t.Fatalf("view is %d rows tall", got)
	}
}

// Up and down move inside a role, and left and right change role, coming back
// to the row the cursor last held there.
func TestModelRoleNavigation(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	a := newModelScreen(t, t.TempDir())
	rows := a.modelRows()

	lo, hi := modelRoleRange(rows, roleAgent)
	a.modelState.selected = hi
	_, _ = a.handleModelKey(tea.KeyMsg{Type: tea.KeyDown})
	if a.modelState.selected != hi {
		t.Fatalf("down crossed out of the agent role: %d, want %d", a.modelState.selected, hi)
	}
	a.modelState.selected = lo
	_, _ = a.handleModelKey(tea.KeyMsg{Type: tea.KeyUp})
	if a.modelState.selected != lo {
		t.Fatalf("up crossed out of the agent role: %d, want %d", a.modelState.selected, lo)
	}

	a.modelState.selected = lo + 1
	_, _ = a.handleModelKey(tea.KeyMsg{Type: tea.KeyRight})
	if got := safeRow(rows, a.modelState.selected).role; got != roleFast {
		t.Fatalf("right went to %q, want fast", got)
	}
	_, _ = a.handleModelKey(tea.KeyMsg{Type: tea.KeyLeft})
	if a.modelState.selected != lo+1 {
		t.Fatalf("left returned to row %d, want the remembered %d", a.modelState.selected, lo+1)
	}
	// Wraps at both ends.
	_, _ = a.handleModelKey(tea.KeyMsg{Type: tea.KeyLeft})
	if got := safeRow(rows, a.modelState.selected).role; got != rolePosture {
		t.Fatalf("left from the first role went to %q, want posture", got)
	}
	_, _ = a.handleModelKey(tea.KeyMsg{Type: tea.KeyRight})
	if got := safeRow(rows, a.modelState.selected).role; got != roleAgent {
		t.Fatalf("right from the last role went to %q, want agent", got)
	}
}

func settingsAt(t *testing.T, scope config.Scope, workdir string) config.Settings {
	t.Helper()
	doc, err := config.OpenSettings(scope, workdir)
	if err != nil {
		t.Fatal(err)
	}
	return doc.Settings
}

// Each role saves to the file its "saves to" chip names, and to no other.
func TestModelScopedSavesLandInTheirFiles(t *testing.T) {
	for _, scope := range []string{"global", "project"} {
		scope := scope
		cfgScope := config.ScopeProject
		other := config.ScopeGlobal
		if scope == "global" {
			cfgScope, other = config.ScopeGlobal, config.ScopeProject
		}

		t.Run("agent effort "+scope, func(t *testing.T) {
			t.Setenv("BELAI_HOME", t.TempDir())
			a := newModelScreen(t, t.TempDir())
			a.modelState.agentScope = scope
			selectRow(t, a, roleAgent, "reasoning")
			_ = a.changeModelRow()
			if got := settingsAt(t, cfgScope, a.workdir).Effort; got != "none" {
				t.Fatalf("%s effort = %q, want none", scope, got)
			}
			if got := settingsAt(t, other, a.workdir).Effort; got != "" {
				t.Fatalf("the other layer was written: effort = %q", got)
			}
		})

		t.Run("classifier tier "+scope, func(t *testing.T) {
			t.Setenv("BELAI_HOME", t.TempDir())
			a := newModelScreen(t, t.TempDir())
			a.modelState.classifierScope = scope
			selectRow(t, a, roleClassifier, "tier")
			_ = a.changeModelRow()
			cls := settingsAt(t, cfgScope, a.workdir).Classifier
			if cls == nil || cls.Tier != config.ClassifierTierFast {
				t.Fatalf("%s classifier = %+v, want tier fast", scope, cls)
			}
			if o := settingsAt(t, other, a.workdir).Classifier; o != nil && o.Tier != "" {
				t.Fatalf("the other layer was written: %+v", o)
			}
		})

		t.Run("routing kind and fast tier "+scope, func(t *testing.T) {
			t.Setenv("BELAI_HOME", t.TempDir())
			a := newModelScreen(t, t.TempDir())
			a.modelState.routingScope = scope
			selectRow(t, a, roleRouting, "kind")
			_ = a.changeModelRow()
			_ = a.stageRouting(roleFast, "model", "fast = openrouter · fast-model", func(r *config.RoutingSettings) {
				r.Fast = &config.RoutingTarget{Provider: "openrouter", Model: "fast-model"}
			})
			r := settingsAt(t, cfgScope, a.workdir).Routing
			if r == nil || r.Kind != config.RoutingRouted || r.Fast == nil || r.Fast.Model != "fast-model" {
				t.Fatalf("%s routing = %+v, want routed with a fast model", scope, r)
			}
			if o := settingsAt(t, other, a.workdir).Routing; o != nil && (o.Kind != "" || o.Fast != nil) {
				t.Fatalf("the other layer was written: %+v", o)
			}
		})
	}

	t.Run("agent effort session", func(t *testing.T) {
		t.Setenv("BELAI_HOME", t.TempDir())
		a := newModelScreen(t, t.TempDir())
		a.modelState.agentScope = "session"
		selectRow(t, a, roleAgent, "reasoning")
		_ = a.changeModelRow()
		st, err := config.LoadState()
		if err != nil {
			t.Fatal(err)
		}
		if st.Effort != "none" {
			t.Fatalf("state effort = %q, want none", st.Effort)
		}
		for _, sc := range []config.Scope{config.ScopeGlobal, config.ScopeProject} {
			if got := settingsAt(t, sc, a.workdir).Effort; got != "" {
				t.Fatalf("a session edit wrote a settings file: effort = %q", got)
			}
		}
	})

	t.Run("posture goes to project prefs", func(t *testing.T) {
		t.Setenv("BELAI_HOME", t.TempDir())
		a := newModelScreen(t, t.TempDir())
		selectRow(t, a, rolePosture, "caveman")
		_ = a.changeModelRow()
		prefs, err := config.LoadProjectPrefs(a.workdir)
		if err != nil {
			t.Fatal(err)
		}
		if prefs.Caveman == nil || !*prefs.Caveman {
			t.Fatalf("prefs = %+v, want caveman on", prefs)
		}
		for _, sc := range []config.Scope{config.ScopeGlobal, config.ScopeProject} {
			if settingsAt(t, sc, a.workdir).CavemanEnabled() {
				t.Fatalf("a posture toggle wrote the %v settings file", sc)
			}
		}
	})
}

// The scope chip and the rail always name where an edit will land.
func TestModelRailNamesEachSaveTarget(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	a := newModelScreen(t, t.TempDir())
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	a.modelState.agentScope = "global"
	a.modelState.classifierScope = "project"
	a.modelState.routingScope = "global"
	rail := ansi.Strip(a.modelView())
	for _, want := range []string{"Agent", "global", "Classifier", "project", "Routing", "Posture", "prefs"} {
		if !strings.Contains(rail, want) {
			t.Fatalf("rail lacks %q\n%s", want, rail)
		}
	}
	// Cycling the scope with s is reflected on the rail at once.
	selectRow(t, a, roleAgent, "provider")
	a.modelState.agentScope = "session"
	_, _ = a.handleModelKey(modelKey("s"))
	if a.modelState.agentScope != "global" {
		t.Fatalf("agent scope = %q, want global after s", a.modelState.agentScope)
	}
}

// failingTester fails its one step with detail and offers a retry.
func failingTester(detail string) modelTester {
	return func(ctx context.Context, steps []modeltest.Step, env *modeltest.Env, emit func(modeltest.Event)) modeltest.Report {
		out := modeltest.Outcome{Status: modeltest.StatusFail, Detail: detail,
			Hints: []modeltest.Hint{{Key: "r", Text: "run the test again", Action: modeltest.ActRetry}}}
		emit(modeltest.Event{Kind: modeltest.EventDone, Index: 0, Outcome: out})
		return modeltest.Report{Passed: false, Steps: []modeltest.StepResult{{Name: "llama-server", Outcome: out}}}
	}
}

// Jev is a decision backend, so it is its own classifier kind: cycling reaches
// it, it starts on OpenRouter's Jev model, it offers only decision backends,
// and leaving it hands the guard back to the main model.
func TestModelClassifierKindJevIsItsOwnKind(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("OPENROUTER_API_KEY", "")
	a := newModelScreen(t, t.TempDir())
	a.modelState.classifierScope = "project"
	a.settings.Classifier = &config.ClassifierSettings{Kind: "llm"}
	if err := config.Mutate(config.ScopeProject, a.workdir, func(s *config.Settings) error {
		s.Classifier = &config.ClassifierSettings{Kind: "llm"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.reloadSettings(); err != nil {
		t.Fatal(err)
	}

	selectRow(t, a, roleClassifier, "kind")
	if got := a.modelRows()[a.modelState.selected].opts; strings.Join(got, ",") != "llm,models,openrouter-decisions,jev" {
		t.Fatalf("kind options = %v, want llm, models, openrouter-decisions, jev", got)
	}
	_ = a.changeModelRow() // llm -> models
	if k := projectClassifier(t, a).Kind; k != "models" {
		t.Fatalf("kind = %q, want models", k)
	}
	selectRow(t, a, roleClassifier, "kind")
	_ = a.changeModelRow() // models -> openrouter-decisions, without any OpenRouter key
	cls := projectClassifier(t, a)
	if cls.Kind != "openrouter-decisions" || cls.Provider != "openrouter" || cls.Model != "typesafe/jev-1.13" {
		t.Fatalf("after jev: %+v", cls)
	}
	if a.classifierKind() != "openrouter-decisions" {
		t.Fatalf("kind row reads %q, want openrouter-decisions", a.classifierKind())
	}
	if got := strings.Join(a.classifierProviders(), ","); !strings.HasPrefix(got, "openrouter") || strings.Contains(got, "ollama") {
		t.Fatalf("jev providers = %s, want decision backends only", got)
	}

	// A model list for openrouter under jev is the Jev models alone.
	a.modelState.pickingRole = roleClassifier
	_, catalog := a.modelPickerCatalog()
	if len(catalog) == 0 {
		t.Fatal("no Jev model offered")
	}
	for _, m := range catalog {
		if !strings.HasPrefix(m.ID, "typesafe/jev") {
			t.Fatalf("jev kind offered a chat model: %s", m.ID)
		}
	}

	// Jev is the native TypeSafe kind: it starts on the hosted provider and
	// offers no OpenRouter.
	selectRow(t, a, roleClassifier, "kind")
	_ = a.changeModelRow() // openrouter-decisions -> jev
	cls = projectClassifier(t, a)
	if cls.Kind != "jev" || cls.Provider != "typesafe" || cls.Model != "jev-latest" {
		t.Fatalf("after jev: %+v", cls)
	}
	if got := strings.Join(a.classifierProviders(), ","); !strings.HasPrefix(got, "typesafe") || strings.Contains(got, "openrouter") {
		t.Fatalf("jev providers = %s", got)
	}

	// Leaving a decision kind clears the decision selection, even though the
	// OpenRouter key is missing, which is what used to leave the row stuck.
	selectRow(t, a, roleClassifier, "kind")
	_ = a.changeModelRow() // jev -> llm
	cls = projectClassifier(t, a)
	if cls.Kind != "llm" || cls.Provider != "" || cls.Model != "" {
		t.Fatalf("after leaving jev: %+v", cls)
	}
	a.modelState.pickingRole = roleClassifier
	_, chat := a.modelPickerCatalog()
	for _, m := range chat {
		if strings.HasPrefix(m.ID, "typesafe/jev") {
			t.Fatalf("kind llm offered Jev: %s", m.ID)
		}
	}
}
