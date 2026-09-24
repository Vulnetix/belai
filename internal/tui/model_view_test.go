package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

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
	if len(rows) != 27 {
		t.Fatalf("len(rows) = %d, want 27", len(rows))
	}
	if rows[0].role != roleAgent || rows[0].key != "provider" {
		t.Fatalf("first row = %+v, want agent provider", rows[0])
	}
	if rows[6].role != roleClassifier || rows[6].key != "kind" {
		t.Fatalf("classifier kind row = %+v", rows[6])
	}
	if rows[13].role != roleRouting || rows[13].key != "kind" {
		t.Fatalf("routing kind row = %+v", rows[13])
	}
	if last := rows[len(rows)-1]; last.role != rolePosture {
		t.Fatalf("last row = %+v, want the posture group", last)
	}
	// The save target lives on the group header; no group carries a scope row.
	for _, r := range rows {
		if r.key == "scope" {
			t.Fatalf("unexpected scope row: %+v", r)
		}
	}
}

// TestModelPostureScopeIsFixed pins the posture group's save target: the
// toggles always write per-project preferences, so `s` must not cycle it
// or any other role's scope.
func TestModelPostureScopeIsFixed(t *testing.T) {
	a := New(Options{})
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	_ = a.enterModel()
	selectRow(t, a, rolePosture, "guardrails")
	before := a.modelState
	_ = a.cycleScope()
	if a.modelState.agentScope != before.agentScope ||
		a.modelState.classifierScope != before.classifierScope ||
		a.modelState.routingScope != before.routingScope {
		t.Fatalf("s on a posture row changed a scope: %+v", a.modelState)
	}
	if got := a.roleScope(rolePosture); got != postureScope {
		t.Fatalf("posture scope = %q, want %q", got, postureScope)
	}
}

// TestModelSetInShownOnlyWhenItDiffers pins the provenance rule: a value set
// in the layer the group saves to is not labelled, and one set in a layer
// that outranks the save target is.
func TestModelSetInShownOnlyWhenItDiffers(t *testing.T) {
	cases := []struct {
		src, scope, want string
		outranks         bool
	}{
		{"default", "project", "", false},
		{"", "session", "", false},
		{"project", "project", "", false},
		{"state", "session", "", false},
		{"project_prefs", postureScope, "", false},
		{"project", "session", "project", true},
		{"project", "global", "project", true},
		{"global", "project", "global", false},
		{"env", "project", "env", true},
	}
	for _, c := range cases {
		if got := setIn(c.src, c.scope); got != c.want {
			t.Errorf("setIn(%q, %q) = %q, want %q", c.src, c.scope, got, c.want)
		}
		if c.want != "" {
			if got := outranks(c.src, c.scope); got != c.outranks {
				t.Errorf("outranks(%q, %q) = %v, want %v", c.src, c.scope, got, c.outranks)
			}
		}
	}
}

// TestModelViewFitsWidth renders the page at several widths and fails on any
// line wider than the terminal — the overlap the fixed 12-cell label column
// and 41-cell value cut used to produce.
func TestModelViewFitsWidth(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()
	if err := config.Mutate(config.ScopeProject, workdir, func(s *config.Settings) error {
		s.Routing = &config.RoutingSettings{
			Kind: config.RoutingRouted,
			UseCases: map[string]config.RoutingTarget{
				"goal_contract": {Provider: "openrouter"},
				"session_name":  {Provider: "openrouter", Model: "some-vendor/a-very-long-model-identifier-that-keeps-going-0731"},
			},
		}
		return nil
	}); err != nil {
		t.Fatalf("seed routing: %v", err)
	}
	for _, width := range []int{80, 120, 200} {
		a := New(Options{Workdir: workdir})
		a.Update(tea.WindowSizeMsg{Width: width, Height: 60})
		_ = a.enterModel()
		view := a.modelView()
		for _, line := range strings.Split(view, "\n") {
			if got := ansi.StringWidth(line); got > width {
				t.Fatalf("width %d: line is %d cells:\n%q", width, got, ansi.Strip(line))
			}
		}
		for _, want := range []string{"saves to", "candidate pool", "SESSION POSTURE", "(default model)", "not in pool"} {
			if !strings.Contains(ansi.Strip(view), want) {
				t.Fatalf("width %d: view missing %q", width, want)
			}
		}
	}
}

func TestModelAgentScopeCanBeCycled(t *testing.T) {
	a := New(Options{})
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	_ = a.enterModel()
	a.modelState.rows = a.modelRows()
	selectRow(t, a, roleAgent, "model")
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
	selectRow(t, a, roleClassifier, "reasoning")
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
	selectRow(t, a, roleAgent, "provider")
	viewAgent := a.modelView()
	if !strings.Contains(viewAgent, "AGENT") {
		t.Fatal("expected AGENT group header")
	}
	if !strings.Contains(viewAgent, "session") {
		t.Fatal("expected agent session chip")
	}

	// Select a classifier row and render again.
	selectRow(t, a, roleClassifier, "kind")
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
	selectRow(t, a, roleAgent, "provider")
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
	selectRow(t, a, roleClassifier, "provider")
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
