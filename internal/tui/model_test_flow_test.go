package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/models"
	"github.com/vulnetix/belai/internal/modeltest"
)

// flowScreen is a /model screen whose tests run in the background through a
// fake tester, like the real flow.
func flowScreen(t *testing.T, tester modelTester) *App {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("BELAI_MODELS_DIR", t.TempDir())
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	t.Setenv("OPENAI_API_KEY", "sk-test")
	a := newModelScreen(t, t.TempDir())
	a.modelState.classifierScope = "project"
	a.modelState.routingScope = "project"
	a.modelTestAsync = true
	a.modelTester = tester
	return a
}

// pump feeds the running test's messages to the app until it finishes.
func pump(t *testing.T, a *App) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case msg := <-a.modelTest.events:
			a.Update(msg)
			if _, done := msg.(modelTestDoneMsg); done {
				return
			}
		case <-deadline:
			t.Fatal("test run did not finish")
		}
	}
}

func projectClassifier(t *testing.T, a *App) *config.ClassifierSettings {
	t.Helper()
	doc, err := config.OpenSettings(config.ScopeProject, a.workdir)
	if err != nil {
		t.Fatal(err)
	}
	return doc.Settings.Classifier
}

func passAfter(gate chan struct{}) modelTester {
	return func(ctx context.Context, steps []modeltest.Step, env *modeltest.Env, emit func(modeltest.Event)) modeltest.Report {
		emit(modeltest.Event{Kind: modeltest.EventStart, Index: 0, Name: "probe"})
		<-gate
		emit(modeltest.Event{Kind: modeltest.EventDone, Index: 0, Name: "probe", Outcome: modeltest.Outcome{Status: modeltest.StatusOK, Detail: "fine"}})
		return modeltest.Report{Passed: true}
	}
}

func TestStagedClassifierIsWrittenOnlyAfterPass(t *testing.T) {
	gate := make(chan struct{})
	a := flowScreen(t, passAfter(gate))
	_ = a.stageClassifier("kind", "classifier.kind = llm", func(c *config.ClassifierSettings) { c.Kind = "llm" })
	if a.modelTest == nil || a.modelTest.outcome != "running" {
		t.Fatal("no test running")
	}
	if cls := projectClassifier(t, a); cls != nil && cls.Kind == "llm" {
		t.Fatal("the change was written before its test finished")
	}
	view := a.modelView()
	if !strings.Contains(view, "testing classifier.kind = llm") || !strings.Contains(view, "nothing is saved until every check passes") {
		t.Fatalf("running panel missing:\n%s", view)
	}
	close(gate)
	pump(t, a)
	if cls := projectClassifier(t, a); cls == nil || cls.Kind != "llm" {
		t.Fatalf("not written after a pass: %+v", cls)
	}
	if a.modelTest.outcome != "saved" || !strings.Contains(a.modelView(), "✓ saved classifier.kind = llm → ") {
		t.Fatalf("outcome %q\n%s", a.modelTest.outcome, a.modelView())
	}
}

func TestFailedTestWritesNothingAndOffersRetry(t *testing.T) {
	calls := 0
	a := flowScreen(t, func(ctx context.Context, steps []modeltest.Step, env *modeltest.Env, emit func(modeltest.Event)) modeltest.Report {
		calls++
		emit(modeltest.Event{Kind: modeltest.EventStart, Index: 0, Name: "probe"})
		out := modeltest.Outcome{Status: modeltest.StatusFail, Detail: "llama-server is not on PATH",
			Hints: []modeltest.Hint{{Text: "install llama.cpp"}, {Key: "r", Text: "run the test again", Action: modeltest.ActRetry}}}
		emit(modeltest.Event{Kind: modeltest.EventDone, Index: 0, Outcome: out})
		return modeltest.Report{Passed: false, Steps: []modeltest.StepResult{{Name: "llama-server", Outcome: out}}}
	})
	_ = a.stageClassifier("kind", "classifier.kind = llm", func(c *config.ClassifierSettings) { c.Kind = "llm" })
	pump(t, a)
	if cls := projectClassifier(t, a); cls != nil && cls.Kind == "llm" {
		t.Fatal("a failed test wrote the change")
	}
	view := a.modelView()
	for _, want := range []string{"✗ not saved", "stays in effect", "install llama.cpp", "run the test again"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view lacks %q:\n%s", want, view)
		}
	}
	a.handleModelKey(modelKey("r"))
	if a.modelTest.outcome != "running" {
		t.Fatalf("r did not retry: outcome=%s", a.modelTest.outcome)
	}
	pump(t, a)
	if calls != 2 {
		t.Fatalf("tester ran %d times, want 2", calls)
	}
}

func TestDownloadConfirmYesAndNo(t *testing.T) {
	for _, tc := range []struct {
		key  string
		want bool
	}{{"y", true}, {"n", false}} {
		answered := make(chan bool, 1)
		a := flowScreen(t, func(ctx context.Context, steps []modeltest.Step, env *modeltest.Env, emit func(modeltest.Event)) modeltest.Report {
			offer := modeltest.DownloadOffer{Label: "Decider-4B", What: "Mapika/decider-4b-GGUF · decider-4b-v2.1-Q4_K_M.gguf", Size: 2708804640}
			emit(modeltest.Event{Kind: modeltest.EventConfirm, Index: 0, Offer: &offer})
			ok := env.Confirm(ctx, offer)
			answered <- ok
			return modeltest.Report{Passed: ok}
		})
		_ = a.stageClassifier("kind", "classifier.kind = llm", func(c *config.ClassifierSettings) { c.Kind = "llm" })
		// Deliver the confirm event, then answer it with a key.
		a.Update(<-a.modelTest.events)
		if a.modelTest.confirm == nil || !strings.Contains(a.modelView(), "Download Mapika/decider-4b-GGUF") || !strings.Contains(a.modelView(), "2.5 GB") {
			t.Fatalf("confirm prompt missing:\n%s", a.modelView())
		}
		a.handleModelKey(modelKey(tc.key))
		if got := <-answered; got != tc.want {
			t.Fatalf("%s answered %v", tc.key, got)
		}
		pump(t, a)
	}
}

func TestEscCancelsRunningTestAndStaleResultIsDropped(t *testing.T) {
	gate := make(chan struct{})
	a := flowScreen(t, func(ctx context.Context, steps []modeltest.Step, env *modeltest.Env, emit func(modeltest.Event)) modeltest.Report {
		select {
		case <-ctx.Done():
		case <-gate:
		}
		return modeltest.Report{Passed: true}
	})
	_ = a.stageClassifier("kind", "classifier.kind = llm", func(c *config.ClassifierSettings) { c.Kind = "llm" })
	before := a.view
	a.handleModelKey(tea.KeyMsg{Type: tea.KeyEsc})
	if a.modelTest.outcome != "cancelled" || a.view != before {
		t.Fatalf("esc should cancel the test and stay on /model: outcome=%s view=%v", a.modelTest.outcome, a.view)
	}
	pump(t, a)
	if cls := projectClassifier(t, a); cls != nil && cls.Kind == "llm" {
		t.Fatal("a cancelled test wrote the change")
	}

	// A result from a superseded run never saves.
	a.handleModelTestDone(modelTestDoneMsg{gen: a.modelTestGen - 1, rep: modeltest.Report{Passed: true}})
	if cls := projectClassifier(t, a); cls != nil && cls.Kind == "llm" {
		t.Fatal("a stale result wrote the change")
	}
	close(gate)
}

func TestKnobsStillWriteImmediately(t *testing.T) {
	a := flowScreen(t, func(context.Context, []modeltest.Step, *modeltest.Env, func(modeltest.Event)) modeltest.Report {
		t.Fatal("a knob must not run a model test")
		return modeltest.Report{}
	})
	_ = a.mutateClassifier(func(c *config.ClassifierSettings) { c.Effort = "low" })
	if cls := projectClassifier(t, a); cls == nil || cls.Effort != "low" {
		t.Fatalf("effort not written: %+v", cls)
	}
	if a.modelTest != nil {
		t.Fatal("a knob started a test")
	}
}

func TestDecisionLocalIsOfferedAndOpensPicker(t *testing.T) {
	a := flowScreen(t, passAfter(make(chan struct{})))
	// Kind llm is chat only: a decision backend is offered under jev, never here.
	for _, p := range a.classifierProviders() {
		if p == decisions.LocalProvider {
			t.Fatal("kind llm offered the decision provider")
		}
	}
	a.settings.Classifier = &config.ClassifierSettings{Kind: "openrouter-decisions"}
	provs := a.classifierProviders()
	if provs[len(provs)-1] != decisions.LocalProvider {
		t.Fatalf("classifier providers %v lack %s", provs, decisions.LocalProvider)
	}
	for _, p := range a.modelProviders() {
		if p == decisions.LocalProvider {
			t.Fatal("the decision provider is offered to a chat role")
		}
	}
	// Choosing it opens the picker on its catalogue instead of saving.
	_ = a.selectClassifierProvider(decisions.LocalProvider)
	if !a.modelState.picking || a.pickerProvider(roleClassifier) != decisions.LocalProvider {
		t.Fatalf("picking=%v provider=%q", a.modelState.picking, a.pickerProvider(roleClassifier))
	}
	name, cat := a.modelPickerCatalog()
	if name != decisions.LocalProvider || len(cat) != 2 || cat[0].ID != "decider-4b" || !strings.Contains(cat[0].Label, "download") {
		t.Fatalf("catalog %s %+v", name, cat)
	}
	if !strings.Contains(a.modelView(), "decision-local") {
		t.Fatal("picker does not show the decision provider")
	}
}

func TestLocalDecisionSelectionPlansTheLocalLadder(t *testing.T) {
	var names []string
	a := flowScreen(t, func(ctx context.Context, steps []modeltest.Step, env *modeltest.Env, emit func(modeltest.Event)) modeltest.Report {
		for _, s := range steps {
			names = append(names, s.Name)
		}
		return modeltest.Report{Passed: false}
	})
	_ = a.stageClassifier("model", "classifier = decision-local · decider-4b", func(c *config.ClassifierSettings) {
		c.Provider, c.Model = decisions.LocalProvider, "decider-4b"
	})
	pump(t, a)
	joined := strings.Join(names, ",")
	for _, want := range []string{"llama-server", "weights", "start", "answer", "sanity", "intent", "speed", "fallback · sentinel"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("plan %v lacks %s", names, want)
		}
	}
	if a.modelTest.change.decisionModel == nil || a.modelTest.change.decisionModel.ID != "decider-4b" {
		t.Fatal("the staged change does not know its decision model")
	}
}

func TestUnknownLocalModelFailsBeforeTesting(t *testing.T) {
	a := flowScreen(t, func(context.Context, []modeltest.Step, *modeltest.Env, func(modeltest.Event)) modeltest.Report {
		t.Fatal("an unresolvable selection must not reach the tester")
		return modeltest.Report{}
	})
	_ = a.stageClassifier("model", "x", func(c *config.ClassifierSettings) { c.Provider, c.Model = decisions.LocalProvider, "gpt-5" })
	if a.modelTest.outcome != "not-saved" || !strings.Contains(a.modelTest.steps[0].detail, "not a local decision model") {
		t.Fatalf("outcome %s detail %q", a.modelTest.outcome, a.modelTest.steps[0].detail)
	}
}

func TestPickerShowsLoadingAndErrors(t *testing.T) {
	a := flowScreen(t, nil)
	a.modelState.picking = true
	a.modelState.pickingRole = roleAgent
	name := "zz-local"
	a.setPendingProvider(roleAgent, name)
	a.catalogCache = map[string][]models.Model{}
	a.catalogLoading = map[string]bool{name: true}
	if !strings.Contains(a.modelPicker(), "loading models") {
		t.Fatalf("no loading state:\n%s", a.modelPicker())
	}
	a.catalogLoading = map[string]bool{}
	a.catalogErr = map[string]string{name: "HTTP 401"}
	if !strings.Contains(a.modelPicker(), "could not list models: HTTP 401") {
		t.Fatalf("no error state:\n%s", a.modelPicker())
	}
}

func jevForm(t *testing.T, a *App) {
	t.Helper()
	a.openProviderNew()
	a.applyProviderNewKind(config.JevKind)
	a.setProviderNewField("protocol", "http")
	a.setProviderNewField("host", "127.0.0.1")
	a.setProviderNewField("port", "8090")
	a.refreshProviderNewDerived()
	if a.providerNewFieldValue("path") != "/v1/systemone" {
		t.Fatalf("path prefill %q", a.providerNewFieldValue("path"))
	}
}

func globalSettings(t *testing.T, a *App) config.Settings {
	t.Helper()
	doc, err := config.OpenSettings(config.ScopeGlobal, a.workdir)
	if err != nil {
		t.Fatal(err)
	}
	return doc.Settings
}

func TestJevProviderSavedOnlyAfterItsTestPasses(t *testing.T) {
	a := flowScreen(t, func(ctx context.Context, steps []modeltest.Step, env *modeltest.Env, emit func(modeltest.Event)) modeltest.Report {
		return modeltest.Report{Passed: false, Steps: []modeltest.StepResult{{Name: "connect", Outcome: modeltest.Outcome{Status: modeltest.StatusFail, Detail: "nothing is listening"}}}}
	})
	jevForm(t, a)
	_, cmd := a.providerNewCommit()
	_ = cmd
	pump(t, a)
	if _, ok := globalSettings(t, a).Providers["p-127-0-0-1-8090"]; ok {
		t.Fatal("a failed test left the provider behind")
	}
	if a.view != viewModel || a.modelTest.outcome != "not-saved" {
		t.Fatalf("view=%v outcome=%s", a.view, a.modelTest.outcome)
	}

	a.modelTester = func(ctx context.Context, steps []modeltest.Step, env *modeltest.Env, emit func(modeltest.Event)) modeltest.Report {
		return modeltest.Report{Passed: true}
	}
	jevForm(t, a)
	_, _ = a.providerNewCommit()
	pump(t, a)
	s := globalSettings(t, a)
	p, ok := s.Providers["p-127-0-0-1-8090"]
	if !ok || p.Kind != config.JevKind || p.BaseURL != "http://127.0.0.1:8090" {
		t.Fatalf("provider not saved after a pass: %+v", s.Providers)
	}
	if cls := a.settings.Classifier; cls == nil || cls.Provider != "p-127-0-0-1-8090" {
		t.Fatalf("classifier not set to the new Jev: %+v", cls)
	}
}

func TestJevProviderRefusesPlainHTTPOffLoopback(t *testing.T) {
	a := flowScreen(t, nil)
	jevForm(t, a)
	a.setProviderNewField("host", "jev.example.com")
	a.refreshProviderNewDerived()
	_, _ = a.providerNewCommit()
	if !strings.Contains(a.providerNewState.errorMsg, "in the clear") {
		t.Fatalf("error %q", a.providerNewState.errorMsg)
	}
}

// A kind whose test fails saves nothing, and the next press of the kind row
// moves on from the kind that was tried, so a backend that cannot be reached
// (no key, no network) never traps the row.
func TestClassifierKindCyclesPastAFailedTest(t *testing.T) {
	calls := 0
	tester := func(ctx context.Context, steps []modeltest.Step, env *modeltest.Env, emit func(modeltest.Event)) modeltest.Report {
		calls++
		emit(modeltest.Event{Kind: modeltest.EventStart, Index: 0, Name: "probe"})
		if calls == 1 {
			emit(modeltest.Event{Kind: modeltest.EventDone, Index: 0, Name: "probe", Outcome: modeltest.Outcome{Status: modeltest.StatusFail, Detail: "refused"}})
			return modeltest.Report{Passed: false}
		}
		emit(modeltest.Event{Kind: modeltest.EventDone, Index: 0, Name: "probe", Outcome: modeltest.Outcome{Status: modeltest.StatusOK, Detail: "fine"}})
		return modeltest.Report{Passed: true}
	}
	a := flowScreen(t, tester)
	t.Setenv("OPENROUTER_API_KEY", "or-key")
	seed := &config.ClassifierSettings{Kind: "openrouter-decisions", Provider: "openrouter", Model: "typesafe/jev-1.13"}
	if err := config.Mutate(config.ScopeProject, a.workdir, func(s *config.Settings) error {
		s.Classifier = seed
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.reloadSettings(); err != nil {
		t.Fatal(err)
	}

	selectRow(t, a, roleClassifier, "kind")
	_ = a.changeModelRow() // openrouter-decisions -> jev: fails
	pump(t, a)
	if k := a.classifierKind(); k != "openrouter-decisions" {
		t.Fatalf("a failed test moved the kind to %q", k)
	}
	selectRow(t, a, roleClassifier, "kind")
	_ = a.changeModelRow() // must move on to llm, not retry jev
	pump(t, a)
	if k := a.classifierKind(); k != "llm" {
		t.Fatalf("kind = %q after a second press, want llm (past the failed jev)", k)
	}
	// The trail resets once a kind is saved: the next press moves on from it.
	selectRow(t, a, roleClassifier, "kind")
	_ = a.changeModelRow()
	if k := a.classifierKind(); k != "models" {
		t.Fatalf("kind = %q, want models after llm", k)
	}
}
