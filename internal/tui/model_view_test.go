package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/config"
)

func TestModelViewRendersRolesAndWarning(t *testing.T) {
	a := New(Options{})
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	_ = a.enterModel()
	view := a.modelView()
	if view == "" {
		t.Fatal("modelView returned empty")
	}
	if !strings.Contains(view, "Model Roles") {
		t.Fatal("expected 'Model Roles' header")
	}
	if !strings.Contains(view, "security gate for tool output") {
		t.Fatal("expected classifier security warning")
	}
}

func TestModelRowsReflectSettings(t *testing.T) {
	a := New(Options{})
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	_ = a.enterModel()
	rows := a.modelRows()
	if len(rows) != 30 {
		t.Fatalf("len(rows) = %d, want 30", len(rows))
	}
	if rows[0].role != roleAgent || rows[0].key != "provider" {
		t.Fatalf("first row = %+v, want agent provider", rows[0])
	}
	if rows[11].role != roleClassifier || rows[11].key != "kind" {
		t.Fatalf("classifier kind row = %+v", rows[11])
	}
	if rows[19].role != roleRouting || rows[19].key != "kind" {
		t.Fatalf("routing kind row = %+v", rows[19])
	}
}

func TestModelAgentScopeCanBeCycled(t *testing.T) {
	a := New(Options{})
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	_ = a.enterModel()
	a.modelState.rows = a.modelRows()
	a.modelState.selected = 8 // agent scope row
	_ = a.cycleScope()
	if a.modelState.agentScope != "global" {
		t.Fatalf("agent scope = %q, want global", a.modelState.agentScope)
	}
}

func TestModelClassifierProviderChangeClearsModel(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 40})

	// Seed a classifier block with a model selected.
	if err := config.Mutate(config.ScopeProject, workdir, func(s *config.Settings) error {
		s.Classifier = &config.ClassifierSettings{
			Provider: "openai",
			Model:    "gpt-4",
			Effort:   "medium",
		}
		return nil
	}); err != nil {
		t.Fatalf("seed classifier: %v", err)
	}
	if err := a.reloadSettings(); err != nil {
		t.Fatalf("reload settings: %v", err)
	}
	_ = a.enterModel()

	a.modelState.classifierScope = "project"
	_ = a.cycleClassifierProvider([]string{"anthropic"})
	if a.settings.Classifier.Model != "" {
		t.Fatalf("model = %q, want empty after provider change", a.settings.Classifier.Model)
	}
}

func TestModelClassifierReasoningToggleDrivesEffort(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 40})

	if err := config.Mutate(config.ScopeProject, workdir, func(s *config.Settings) error {
		s.Classifier = &config.ClassifierSettings{
			Provider: "openai",
			Model:    "gpt-4",
			Effort:   "medium",
		}
		return nil
	}); err != nil {
		t.Fatalf("seed classifier: %v", err)
	}
	if err := a.reloadSettings(); err != nil {
		t.Fatalf("reload settings: %v", err)
	}
	_ = a.enterModel()

	// Selected row must be the classifier reasoning toggle.
	a.modelState.rows = a.modelRows()
	a.modelState.selected = 14
	a.modelState.classifierScope = "project"
	_ = a.changeModelRow()
	if a.settings.Classifier.Effort != "none" {
		t.Fatalf("effort = %q, want none after reasoning off", a.settings.Classifier.Effort)
	}

	// Toggle back on; the previous chip should be restored.
	a.modelState.rows = a.modelRows()
	_ = a.changeModelRow()
	if a.settings.Classifier.Effort != "medium" {
		t.Fatalf("effort = %q, want medium after reasoning on", a.settings.Classifier.Effort)
	}
}

func TestModelViewGroupsRolesWithPerRoleBadges(t *testing.T) {
	a := New(Options{})
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	_ = a.enterModel()
	a.modelState.agentScope = "session"
	a.modelState.classifierScope = "project"

	// Select an agent row and render.
	a.modelState.selected = 0
	viewAgent := a.modelView()
	if !strings.Contains(viewAgent, "AGENT") {
		t.Fatal("expected AGENT group header")
	}
	if !strings.Contains(viewAgent, "session") {
		t.Fatal("expected agent session chip")
	}

	// Select a classifier row and render again.
	a.modelState.selected = 11
	viewClassifier := a.modelView()
	if !strings.Contains(viewClassifier, "CLASSIFIER") {
		t.Fatal("expected CLASSIFIER group header")
	}
	if !strings.Contains(viewClassifier, "project") {
		t.Fatal("expected classifier project chip")
	}

	// The agent chip must still be present after moving to the classifier group.
	if !strings.Contains(viewClassifier, "AGENT") || !strings.Contains(viewClassifier, "session") {
		t.Fatal("agent scope badge disappeared when cursor moved to classifier")
	}
}

func TestModelAgentProviderChangeReResolvesConfig(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	t.Setenv("OPENROUTER_API_KEY", "or-key")
	// openrouter is the default provider, so the starting point this test
	// cycles away from has to be named explicitly.
	t.Setenv("SIGNET_PROVIDER", "openai")
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	_ = a.enterModel()
	a.modelState.agentScope = "session"

	if a.cfg.Provider != "openai" {
		t.Fatalf("initial provider = %q, want openai", a.cfg.Provider)
	}
	startBase := a.cfg.BaseURL

	// Cycle the agent provider row until openrouter is selected. Changing the
	// provider must re-resolve the base URL and API key, not just the name.
	providers := a.modelProviders()
	for i := 0; i <= len(providers); i++ {
		_ = a.cycleAgentProvider(providers)
		if a.cfg.Provider == "openrouter" {
			break
		}
	}
	if a.cfg.Provider != "openrouter" {
		t.Fatalf("provider = %q, want openrouter", a.cfg.Provider)
	}
	if a.cfg.BaseURL == startBase || !strings.Contains(a.cfg.BaseURL, "openrouter.ai") {
		t.Fatalf("base URL = %q, want re-resolved openrouter URL (stale base URL %q)", a.cfg.BaseURL, startBase)
	}
	if a.cfg.APIKey != "or-key" {
		t.Fatalf("api key = %q, want or-key", a.cfg.APIKey)
	}
}

func TestModelAgentProviderChangeGlobalScopeReResolvesConfig(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	t.Setenv("OPENROUTER_API_KEY", "or-key")
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	_ = a.enterModel()
	a.modelState.agentScope = "global"

	providers := a.modelProviders()
	for i := 0; i <= len(providers); i++ {
		_ = a.cycleAgentProvider(providers)
		if a.cfg.Provider == "openrouter" {
			break
		}
	}
	if a.cfg.Provider != "openrouter" {
		t.Fatalf("provider = %q, want openrouter", a.cfg.Provider)
	}
	if !strings.Contains(a.cfg.BaseURL, "openrouter.ai") {
		t.Fatalf("base URL = %q, want re-resolved openrouter URL", a.cfg.BaseURL)
	}
	if a.cfg.APIKey != "or-key" {
		t.Fatalf("api key = %q, want or-key", a.cfg.APIKey)
	}
	if a.settings.Provider != "openrouter" {
		t.Fatalf("settings provider = %q, want openrouter persisted", a.settings.Provider)
	}
}

func TestModelScopeKeyCyclesRoleScopeNotRowOptions(t *testing.T) {
	a := New(Options{})
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	_ = a.enterModel()
	a.modelState.agentScope = "session"
	a.modelState.classifierScope = "project"
	a.modelState.rows = a.modelRows()

	// Press scope on the agent provider row, whose opts are provider names.
	// The scope must cycle through role scopes, never provider names.
	a.modelState.selected = 0
	_ = a.cycleScope()
	if a.modelState.agentScope != "global" {
		t.Fatalf("agent scope = %q, want global", a.modelState.agentScope)
	}
	_ = a.cycleScope()
	if a.modelState.agentScope != "project" {
		t.Fatalf("agent scope = %q, want project", a.modelState.agentScope)
	}
	_ = a.cycleScope()
	if a.modelState.agentScope != "session" {
		t.Fatalf("agent scope = %q, want session", a.modelState.agentScope)
	}

	// Same for the classifier provider row.
	a.modelState.selected = 12
	a.modelState.classifierScope = "project"
	_ = a.cycleScope()
	if a.modelState.classifierScope != "global" {
		t.Fatalf("classifier scope = %q, want global", a.modelState.classifierScope)
	}
	_ = a.cycleScope()
	if a.modelState.classifierScope != "project" {
		t.Fatalf("classifier scope = %q, want project", a.modelState.classifierScope)
	}
}

func TestModelClassifierScopeWritesGlobalSettings(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 40})

	if err := config.Mutate(config.ScopeGlobal, workdir, func(s *config.Settings) error {
		s.Classifier = &config.ClassifierSettings{
			Provider: "openai",
			Model:    "gpt-4",
			Effort:   "low",
		}
		return nil
	}); err != nil {
		t.Fatalf("seed global classifier: %v", err)
	}
	if err := a.reloadSettings(); err != nil {
		t.Fatalf("reload settings: %v", err)
	}
	_ = a.enterModel()
	a.modelState.classifierScope = "global"
	_ = a.cycleClassifierEffort([]string{"low", "medium", "high"})
	if a.settings.Classifier.Effort != "medium" {
		t.Fatalf("effort = %q, want medium", a.settings.Classifier.Effort)
	}
}
