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
	if len(rows) != 11 {
		t.Fatalf("len(rows) = %d, want 11", len(rows))
	}
	if rows[0].role != roleAgent || rows[0].key != "provider" {
		t.Fatalf("first row = %+v, want agent provider", rows[0])
	}
	if rows[4].role != roleClassifier || rows[4].key != "provider" {
		t.Fatalf("classifier provider row = %+v", rows[4])
	}
}

func TestModelAgentScopeCanBeCycled(t *testing.T) {
	a := New(Options{})
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	_ = a.enterModel()
	a.modelState.rows = a.modelRows()
	a.modelState.selected = 3 // agent scope row
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

	// Selected row must be the reasoning toggle (index 6).
	a.modelState.rows = a.modelRows()
	a.modelState.selected = 6
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
	a.modelState.selected = 4
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
