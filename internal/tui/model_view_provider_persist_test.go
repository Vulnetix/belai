package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/config"
)

// newPersistProviderApp builds an App with a real resolver and lands an
// availability probe so the /model provider list is deterministic. Both
// BELAI_HOME and the workdir use isolated temp directories.
func newPersistProviderApp(t *testing.T) (*App, string) {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir, Resolver: newTestResolver(t, workdir)})
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	_ = a.enterModel()
	if cmd := a.probeAvailabilityCmd(); cmd != nil {
		m, ok := cmd().(availabilityMsg)
		if !ok {
			t.Fatalf("probe returned %T, want availabilityMsg", m)
		}
		a.handleAvailability(m)
	}
	a.modelState.classifierScope = "project"
	return a, workdir
}

// Changing the provider in /model with session scope persists it to state.json
// so the next launched TUI starts with that provider instead of the
// openrouter default.
func TestModelAgentProviderSessionScopePersistsToState(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "or-key")
	a, workdir := newPersistProviderApp(t)
	a.modelState.agentScope = "session"

	providers := a.modelProviders()
	for i := 0; i <= len(providers); i++ {
		_ = a.cycleAgentProvider(providers)
		if a.cfg.Provider == "openrouter" {
			break
		}
	}
	if a.cfg.Provider != "openrouter" {
		t.Fatalf("provider = %q, want openrouter in this test", a.cfg.Provider)
	}

	st, err := config.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if st.Provider != "openrouter" {
		t.Fatalf("state provider = %q, want openrouter", st.Provider)
	}

	// A fresh app with the same BELAI_HOME and workdir must start with the
	// saved provider. No CLI flags or env overrides are in play.
	b := New(Options{Workdir: workdir})
	if b.cfg.Provider != "openrouter" {
		t.Fatalf("new app provider = %q, want openrouter from state", b.cfg.Provider)
	}
}

// Changing the provider in /model with global scope persists it to
// ~/.belai/settings.json and a fresh TUI picks it up through config.Resolve.
func TestModelAgentProviderGlobalScopePersistsToSettings(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	t.Setenv("OPENROUTER_API_KEY", "or-key")
	a, workdir := newPersistProviderApp(t)
	a.modelState.agentScope = "global"

	providers := a.modelProviders()
	for i := 0; i <= len(providers); i++ {
		_ = a.cycleAgentProvider(providers)
		if a.cfg.Provider == "openrouter" {
			break
		}
	}
	if a.cfg.Provider != "openrouter" {
		t.Fatalf("provider = %q, want openrouter in this test", a.cfg.Provider)
	}

	got, err := config.LoadGlobal()
	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}
	if got.Provider != "openrouter" {
		t.Fatalf("global settings provider = %q, want openrouter", got.Provider)
	}

	b := New(Options{Workdir: workdir})
	if b.cfg.Provider != "openrouter" {
		t.Fatalf("new app provider = %q, want openrouter from global settings", b.cfg.Provider)
	}
}

// Changing the provider in /model with project scope persists it to
// .vulnetix/settings.json and a fresh TUI picks it up through config.Resolve.
func TestModelAgentProviderProjectScopePersistsToSettings(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	t.Setenv("OPENROUTER_API_KEY", "or-key")
	a, workdir := newPersistProviderApp(t)
	a.modelState.agentScope = "project"

	providers := a.modelProviders()
	for i := 0; i <= len(providers); i++ {
		_ = a.cycleAgentProvider(providers)
		if a.cfg.Provider == "openrouter" {
			break
		}
	}
	if a.cfg.Provider != "openrouter" {
		t.Fatalf("provider = %q, want openrouter in this test", a.cfg.Provider)
	}

	got, err := config.LoadProject(workdir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if got.Provider != "openrouter" {
		t.Fatalf("project settings provider = %q, want openrouter", got.Provider)
	}

	b := New(Options{Workdir: workdir})
	if b.cfg.Provider != "openrouter" {
		t.Fatalf("new app provider = %q, want openrouter from project settings", b.cfg.Provider)
	}
}
