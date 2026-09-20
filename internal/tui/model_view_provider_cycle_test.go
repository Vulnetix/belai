package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// newProviderCycleApp builds an App with a real resolver and a landed
// availability probe so the /model provider list is the filtered
// (authenticated) one, not the pre-probe full list.
func newProviderCycleApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir, Resolver: newTestResolver(t, workdir)})
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	_ = a.enterModel()
	// enterModel may have started a probe in flight; availabilityCmdIfStale
	// would then refuse to re-probe, so drive one directly for a synchronous,
	// deterministic cache. The stray background probe (if any) delivers its
	// message to a tea runtime that is not running, so it is inert.
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

// A full lap of the agent provider cycle must visit every offered provider
// exactly once and return to the start: the switcher is meant to cycle all
// authenticated providers, not a sub-segment of them.
func TestAgentProviderCycleCoversEveryProvider(t *testing.T) {
	for _, kv := range [][2]string{
		{"OPENAI_API_KEY", "sk-openai"},
		{"OPENROUTER_API_KEY", "or-key"},
		{"GROQ_API_KEY", "groq-key"},
	} {
		t.Setenv(kv[0], kv[1])
	}
	a := newProviderCycleApp(t)
	a.cfg.Provider = "openai"
	if a.cfg.Provider == "" {
		t.Fatal("openai should be the committed default provider")
	}

	start := a.cfg.Provider
	seen := map[string]int{}
	lap := a.modelProviders()
	for i := 0; i < len(lap); i++ {
		_ = a.cycleAgentProvider(a.modelProviders())
		if a.cfg.Provider == "" {
			t.Fatalf("step %d: cycle produced an empty provider; the ring must not pass through the unset stop", i+1)
		}
		seen[a.cfg.Provider]++
	}
	if a.cfg.Provider != start {
		t.Fatalf("after %d steps provider = %q, want back at %q", len(lap), a.cfg.Provider, start)
	}
	// Every offered provider must have been reached exactly once.
	current := map[string]bool{}
	for _, p := range a.modelProviders() {
		current[p] = true
	}
	for p := range current {
		if seen[p] != 1 {
			t.Fatalf("provider %q visited %d times in a full lap, want exactly 1 (lap started at %q)", p, seen[p], start)
		}
	}
	if len(seen) != len(current) {
		t.Fatalf("lap visited %v, offered list is %v", seen, a.modelProviders())
	}
}

// The reported bug: with the committed provider mid-list, the cycle wrapped
// from the last provider back to the default instead of the first, so
// providers sorting before the committed one were never visited. One full lap
// must include them.
func TestAgentProviderCycleReachesProvidersBeforeCommitted(t *testing.T) {
	for _, kv := range [][2]string{
		{"OPENAI_API_KEY", "sk-openai"},
		{"ANTHROPIC_API_KEY", "ant-key"},
		{"DEEPSEEK_API_KEY", "ds-key"},
	} {
		t.Setenv(kv[0], kv[1])
	}
	a := newProviderCycleApp(t)
	a.cfg.Provider = "openai"
	lap := a.modelProviders()
	if indexOfString(lap, "openai") <= 0 {
		t.Fatalf("test precondition: openai must not be first in %v", lap)
	}

	visited := map[string]bool{}
	for i := 0; i < len(lap); i++ {
		_ = a.cycleAgentProvider(a.modelProviders())
		visited[a.cfg.Provider] = true
	}
	for _, earlier := range []string{"anthropic", "deepseek"} {
		if !visited[earlier] {
			t.Fatalf("provider %q sorts before openai and was never reached in a full lap", earlier)
		}
	}
}

// Wrapping from the last offered provider must land on the first offered
// provider, never on an empty provider and never on the default.
func TestAgentProviderCycleWrapsLastToFirst(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	t.Setenv("XAI_API_KEY", "xai-key")
	a := newProviderCycleApp(t)
	opts := a.modelProviders()
	if len(opts) < 2 {
		t.Fatalf("need at least two offered providers, got %v", opts)
	}
	last := opts[len(opts)-1]
	first := opts[0]
	if last == first {
		t.Fatalf("degenerate list %v", opts)
	}
	a.cfg.Provider = last
	_ = a.cycleAgentProvider(a.modelProviders())
	if a.cfg.Provider != first {
		t.Fatalf("wrap from %q landed on %q, want first offered provider %q", last, a.cfg.Provider, first)
	}
}

// A provider change clears the model: a model id is only meaningful to its
// own provider. The running config then re-resolves to the new provider's
// default model, so the old provider's model id must be gone.
func TestAgentProviderCycleClearsModel(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	t.Setenv("OPENROUTER_API_KEY", "or-key")
	a := newProviderCycleApp(t)
	a.cfg.Provider = "openai"
	a.cfg.Model = "gpt-5"
	_ = a.cycleAgentProvider(a.modelProviders())
	if a.cfg.Provider == "openai" {
		t.Fatal("cycle should have moved off openai")
	}
	if a.cfg.Model == "gpt-5" {
		t.Fatalf("model = %q after provider change, want the old provider's model cleared", a.cfg.Model)
	}
}

// An empty option list is a no-op (modelProviders is contractually never
// empty, but the cycler must not index into it).
func TestAgentProviderCycleEmptyOptsNoop(t *testing.T) {
	a := New(Options{})
	a.cfg.Provider = "openai"
	a.cfg.Model = "gpt-5"
	if cmd := a.cycleAgentProvider(nil); cmd != nil {
		t.Fatal("cycleAgentProvider(nil) must be a no-op")
	}
	if a.cfg.Provider != "openai" || a.cfg.Model != "gpt-5" {
		t.Fatalf("no-op cycle changed state: provider=%q model=%q", a.cfg.Provider, a.cfg.Model)
	}
}

// If the current provider is somehow absent from the offered list, the cycle
// falls back to the first offered provider rather than an out-of-range index.
func TestAgentProviderCycleUnknownCurrentJumpsToFirst(t *testing.T) {
	a := New(Options{})
	a.cfg.Provider = "definitely-not-offered"
	_ = a.cycleAgentProvider([]string{"anthropic", "openai"})
	if a.cfg.Provider != "anthropic" {
		t.Fatalf("provider = %q, want first offered provider anthropic", a.cfg.Provider)
	}
}

// The classifier ring deliberately keeps the inherit stop: "" means the
// classifier follows the main model, and unlike the agent it is a stable
// state (nothing normalises it away), so the cycle wraps through it.
func TestClassifierProviderCycleKeepsInheritStop(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	t.Setenv("OPENROUTER_API_KEY", "or-key")
	a := newProviderCycleApp(t)

	// From the inherit stop the cycle moves to the first offered provider.
	_ = a.cycleClassifierProvider(a.classifierProviders())
	if a.settings.Classifier == nil || a.settings.Classifier.Provider == "" {
		t.Fatalf("from inherit stop provider = %+v, want the first offered provider", a.settings.Classifier)
	}

	// Cycling from the last offered provider wraps back to the inherit stop.
	// A fully zeroed classifier block is normalised away on write, and a nil
	// block and an empty provider are the same state: follow the main model.
	a.modelState.rows = a.modelRows()
	_ = a.cycleClassifierProvider([]string{a.settings.Classifier.Provider})
	if cls := a.settings.Classifier; cls != nil && cls.Provider != "" {
		t.Fatalf("wrap from last landed on %q, want the inherit stop (empty provider)", cls.Provider)
	}
}
