package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/config"
)

// newProviderCycleApp builds an App with a real resolver and a landed
// availability probe so the /model provider list is the filtered
// (authenticated) one, not the pre-probe full list.
func newProviderCycleApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir, Resolver: newTestResolver(t, workdir)})
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	_ = a.enterModel()
	// Drive one probe directly for a synchronous, deterministic cache.
	if cmd := a.probeAvailabilityCmd(); cmd != nil {
		m, ok := cmd().(availabilityMsg)
		if !ok {
			t.Fatalf("probe returned %T, want availabilityMsg", m)
		}
		a.handleAvailability(m)
	}
	a.modelState.agentScope = "session"
	a.modelState.classifierScope = "project"
	return a
}

// commitAgentProviderForTest stages and saves a provider with its default
// model, the way a model pick on that provider does, for persistence tests.
func commitAgentProviderForTest(a *App, name string) {
	_ = a.stageAgent("provider", name, "", func(s *config.Settings) {
		s.Provider = name
		s.Model = ""
	}, func() {
		a.cfg.Provider = name
		a.cfg.Model = ""
	})
}

// The provider list must reach every offered provider by moving the cursor,
// whatever the recorded test results are. This is the regression: a failed
// test pinned the old cycle, making every provider after it unselectable.
func TestProviderPickerReachesEveryProviderDespiteFailedTests(t *testing.T) {
	for _, kv := range [][2]string{
		{"OPENAI_API_KEY", "sk-openai"},
		{"OPENROUTER_API_KEY", "or-key"},
		{"GROQ_API_KEY", "groq-key"},
	} {
		t.Setenv(kv[0], kv[1])
	}
	a := newProviderCycleApp(t)
	a.cfg.Provider = "openai"
	opts := a.modelProviders()
	if len(opts) < 3 {
		t.Fatalf("need three providers, got %v", opts)
	}
	// Every provider has a failed test on record.
	a.providerTests = map[string]string{}
	for _, name := range opts {
		a.providerTests[name] = providerFailed
	}

	_ = a.openProviderPicker(roleAgent, opts, a.cfg.Provider)
	if !a.modelState.pickingProvider {
		t.Fatal("provider picker did not open")
	}
	seen := map[string]bool{opts[a.modelState.providerIdx]: true}
	for i := 0; i < len(opts)-1; i++ {
		a.handleProviderPickerKey(tea.KeyMsg{Type: tea.KeyDown})
		seen[opts[a.modelState.providerIdx]] = true
	}
	if len(seen) != len(opts) {
		t.Fatalf("cursor reached %v of %v", seen, opts)
	}
}

// Enter on a provider whose last test failed still selects it: the model
// picker opens on that provider with the choice pending.
func TestProviderPickerEnterSelectsFailedProvider(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	t.Setenv("OPENROUTER_API_KEY", "or-key")
	a := newProviderCycleApp(t)
	a.cfg.Provider = "openai"
	opts := a.modelProviders()
	target := ""
	for _, n := range opts {
		if n != "openai" {
			target = n
			break
		}
	}
	if target == "" {
		t.Fatalf("need a second provider in %v", opts)
	}
	a.providerTests = map[string]string{target: providerFailed}

	_ = a.openProviderPicker(roleAgent, opts, a.cfg.Provider)
	a.modelState.providerIdx = indexOfString(opts, target)
	a.handleProviderPickerKey(key("enter"))

	if a.modelState.pickingProvider {
		t.Fatal("provider picker should close on enter")
	}
	if !a.modelState.picking || a.pickerProvider(roleAgent) != target {
		t.Fatalf("model picker should be open on %q, picking=%v provider=%q",
			target, a.modelState.picking, a.pickerProvider(roleAgent))
	}
}

// Esc leaves the committed provider alone.
func TestProviderPickerEscKeepsCommittedProvider(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	a := newProviderCycleApp(t)
	a.cfg.Provider = "openai"
	_ = a.openProviderPicker(roleAgent, a.modelProviders(), a.cfg.Provider)
	a.handleProviderPickerKey(tea.KeyMsg{Type: tea.KeyEsc})
	if a.modelState.pickingProvider || a.cfg.Provider != "openai" {
		t.Fatalf("esc changed state: picking=%v provider=%q", a.modelState.pickingProvider, a.cfg.Provider)
	}
}

// An empty option list is a no-op.
func TestProviderPickerEmptyOptsNoop(t *testing.T) {
	a := New(Options{})
	if cmd := a.openProviderPicker(roleAgent, nil, ""); cmd != nil || a.modelState.pickingProvider {
		t.Fatal("empty provider list must not open the picker")
	}
}

// The classifier list keeps the inherit entry: "" follows the main model.
func TestClassifierProviderPickerKeepsInheritEntry(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	a := newProviderCycleApp(t)
	_ = a.selectClassifierProvider("openai")
	if a.settings.Classifier == nil || a.settings.Classifier.Provider != "openai" {
		t.Fatalf("classifier = %+v, want provider openai", a.settings.Classifier)
	}
	_ = a.selectClassifierProvider("")
	if cls := a.settings.Classifier; cls != nil && cls.Provider != "" {
		t.Fatalf("classifier provider = %q, want inherit (empty)", cls.Provider)
	}
}
