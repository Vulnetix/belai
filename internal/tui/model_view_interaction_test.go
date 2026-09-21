package tui

import (
	"os"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/models"
	"github.com/vulnetix/signet/internal/run"
)

// modelKey builds a rune key message, matching the key handlers' m.String()
// switch on single characters.
func modelKey(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

// newModelScreen builds an App, sizes it, and enters the /model screen so the
// role rows and model state are initialised.
func newModelScreen(t *testing.T, workdir string) *App {
	t.Helper()
	a := New(Options{Workdir: workdir})
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	_ = a.enterModel()
	return a
}

// The scope key ('s') must cycle the selected role's own scope list. It must
// never read the selected row's options: on the provider row those are provider
// names, and a previous implementation wrote the first provider name into the
// scope field.
func TestModelScopeKeyCyclesRoleScopes(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := newModelScreen(t, t.TempDir())
	a.modelState.agentScope = "session"
	a.modelState.classifierScope = "project"

	a.modelState.selected = 0 // agent provider row
	for _, want := range []string{"global", "project", "session"} {
		_, _ = a.handleModelKey(modelKey("s"))
		if a.modelState.agentScope != want {
			t.Fatalf("agent scope = %q, want %q", a.modelState.agentScope, want)
		}
	}

	a.modelState.selected = 4 // classifier provider row
	a.modelState.classifierScope = "project"
	for _, want := range []string{"global", "project"} {
		_, _ = a.handleModelKey(modelKey("s"))
		if a.modelState.classifierScope != want {
			t.Fatalf("classifier scope = %q, want %q", a.modelState.classifierScope, want)
		}
	}
}

func TestModelKeyNavigationBounds(t *testing.T) {
	a := newModelScreen(t, t.TempDir())
	n := len(a.modelRows())

	a.modelState.selected = 0
	_, _ = a.handleModelKey(tea.KeyMsg{Type: tea.KeyDown})
	if a.modelState.selected != 1 {
		t.Fatalf("selected = %d, want 1", a.modelState.selected)
	}

	a.modelState.selected = n - 1
	_, _ = a.handleModelKey(tea.KeyMsg{Type: tea.KeyDown})
	if a.modelState.selected != n-1 {
		t.Fatalf("selected = %d, want clamped at %d", a.modelState.selected, n-1)
	}

	a.modelState.selected = 0
	_, _ = a.handleModelKey(tea.KeyMsg{Type: tea.KeyUp})
	if a.modelState.selected != 0 {
		t.Fatalf("selected = %d, want clamped at 0", a.modelState.selected)
	}
}

func TestModelKeyEscAndProviders(t *testing.T) {
	a := newModelScreen(t, t.TempDir())

	a.view = viewModel
	a.viewStack = []viewState{viewChat}
	_, _ = a.handleModelKey(tea.KeyMsg{Type: tea.KeyEsc})
	if a.view != viewChat {
		t.Fatalf("view = %v, want viewChat after esc", a.view)
	}

	a.view = viewModel
	a.viewStack = nil
	_, _ = a.handleModelKey(modelKey("p"))
	if a.view != viewProviders {
		t.Fatalf("view = %v, want viewProviders after p", a.view)
	}
}

// Enter on the agent provider row cycles to the next provider and re-resolves
// the wire config: the base URL and API key must follow the provider, not stay
// pinned to the previous one.
func TestModelKeyEnterCyclesAgentProviderAndReResolves(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	t.Setenv("OPENROUTER_API_KEY", "or-key")
	// openrouter is the default provider, so the starting point this test
	// cycles away from has to be named explicitly.
	t.Setenv("SIGNET_PROVIDER", "openai")
	a := newModelScreen(t, t.TempDir())
	a.modelState.agentScope = "session"

	if a.cfg.Provider != "openai" {
		t.Fatalf("initial provider = %q, want openai", a.cfg.Provider)
	}
	startBase := a.cfg.BaseURL

	a.modelState.selected = 0 // agent provider row
	_, _ = a.handleModelKey(tea.KeyMsg{Type: tea.KeyEnter})
	if a.cfg.Provider != "openrouter" {
		t.Fatalf("provider = %q, want openrouter", a.cfg.Provider)
	}
	if a.cfg.BaseURL == startBase || !strings.Contains(a.cfg.BaseURL, "openrouter.ai") {
		t.Fatalf("base URL = %q, want re-resolved openrouter URL (stale %q)", a.cfg.BaseURL, startBase)
	}
	if a.cfg.APIKey != "or-key" {
		t.Fatalf("api key = %q, want or-key", a.cfg.APIKey)
	}
}

func TestModelKeyEnterCyclesAgentEffort(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	a := newModelScreen(t, t.TempDir())
	a.modelState.agentScope = "session"

	a.modelState.selected = 2 // agent effort row
	_, _ = a.handleModelKey(tea.KeyMsg{Type: tea.KeyEnter})
	if a.cfg.Effort != "low" {
		t.Fatalf("effort = %q, want low", a.cfg.Effort)
	}
	if a.settings.Effort != "low" {
		t.Fatalf("settings effort = %q, want low (refreshProvider reads settings)", a.settings.Effort)
	}
}

func TestModelKeyEnterOpensModelPicker(t *testing.T) {
	a := newModelScreen(t, t.TempDir())

	a.modelState.selected = 1 // agent model row
	_, _ = a.handleModelKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !a.modelState.picking {
		t.Fatal("expected the model sub-picker to open")
	}
	if a.modelState.pickingRole != roleAgent {
		t.Fatalf("picking role = %q, want agent", a.modelState.pickingRole)
	}

	// Esc closes it.
	_, _ = a.handleModelKey(tea.KeyMsg{Type: tea.KeyEsc})
	if a.modelState.picking {
		t.Fatal("expected the model sub-picker to close on esc")
	}
}

func TestModelKeyUnsetAgentRows(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	a := newModelScreen(t, t.TempDir())
	a.modelState.agentScope = "session"

	// Unset effort.
	a.settings.Effort = "high"
	a.cfg.Effort = "high"
	a.modelState.selected = 2
	_, _ = a.handleModelKey(modelKey("x"))
	if a.cfg.Effort != "" || a.settings.Effort != "" {
		t.Fatalf("effort not cleared: cfg=%q settings=%q", a.cfg.Effort, a.settings.Effort)
	}

	// Unset provider: the running config re-resolves to the default provider,
	// which is openrouter's free router on an install that names none.
	a.modelState.selected = 0
	_, _ = a.handleModelKey(modelKey("x"))
	if a.cfg.Provider != "openrouter" {
		t.Fatalf("provider = %q, want default openrouter after unset", a.cfg.Provider)
	}
	if a.cfg.Model != "openrouter/free" {
		t.Fatalf("model = %q, want default openrouter/free after unset", a.cfg.Model)
	}
}

func TestModelKeyUnsetClassifierRows(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()
	caveman := true
	if err := config.Mutate(config.ScopeProject, workdir, func(s *config.Settings) error {
		s.Classifier = &config.ClassifierSettings{
			Provider: "openai",
			Model:    "gpt-4",
			Effort:   "medium",
			Caveman:  &caveman,
		}
		return nil
	}); err != nil {
		t.Fatalf("seed classifier: %v", err)
	}

	a := newModelScreen(t, workdir)
	if err := a.reloadSettings(); err != nil {
		t.Fatalf("reload settings: %v", err)
	}
	a.modelState.classifierScope = "project"

	// Unset provider drops the provider+model pair.
	a.modelState.selected = 4
	_, _ = a.handleModelKey(modelKey("x"))
	if a.settings.Classifier.Provider != "" || a.settings.Classifier.Model != "" {
		t.Fatalf("classifier provider/model not cleared: %+v", a.settings.Classifier)
	}

	// Unset effort.
	a.modelState.selected = 7
	_, _ = a.handleModelKey(modelKey("x"))
	if a.settings.Classifier.Effort != "" {
		t.Fatalf("classifier effort = %q, want empty", a.settings.Classifier.Effort)
	}

	// Unset caveman. With no overrides left, the whole classifier block drops
	// (IsZero), so accept either a nil block or a nil caveman pointer.
	a.modelState.selected = 8
	_, _ = a.handleModelKey(modelKey("x"))
	if a.settings.Classifier != nil && a.settings.Classifier.Caveman != nil {
		t.Fatalf("classifier caveman = %v, want cleared", a.settings.Classifier.Caveman)
	}
}

func TestModelPickerNavigationAndSelect(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	// The assertions read the openai catalogue, so the app has to be on openai
	// rather than the openrouter default.
	t.Setenv("SIGNET_PROVIDER", "openai")
	a := newModelScreen(t, t.TempDir())
	a.modelState.agentScope = "session"

	catalog := a.catalogFor("openai")
	if len(catalog) < 2 {
		t.Fatalf("openai catalogue = %d entries, want >= 2", len(catalog))
	}

	a.modelState.picking = true
	a.modelState.pickingRole = roleAgent
	a.modelState.modelIdx = 0

	_, _ = a.handleModelKey(tea.KeyMsg{Type: tea.KeyDown})
	if a.modelState.modelIdx != 1 {
		t.Fatalf("modelIdx = %d, want 1", a.modelState.modelIdx)
	}

	_, _ = a.handleModelKey(tea.KeyMsg{Type: tea.KeyEnter})
	if a.modelState.picking {
		t.Fatal("picker should close after selecting a model")
	}
	if a.cfg.Model != catalog[1].ID {
		t.Fatalf("model = %q, want %q", a.cfg.Model, catalog[1].ID)
	}
}

func TestModelPickerFilterBackspaceEsc(t *testing.T) {
	a := newModelScreen(t, t.TempDir())
	a.modelState.picking = true
	a.modelState.pickingRole = roleAgent

	_, _ = a.handleModelKey(modelKey("/"))
	if !a.modelState.filtering {
		t.Fatal("expected filter mode after /")
	}

	for _, r := range "gpt" {
		_, _ = a.handleModelKey(modelKey(string(r)))
	}
	if a.modelState.filter != "gpt" {
		t.Fatalf("filter = %q, want gpt", a.modelState.filter)
	}

	_, _ = a.handleModelKey(tea.KeyMsg{Type: tea.KeyBackspace})
	if a.modelState.filter != "gp" {
		t.Fatalf("filter = %q, want gp after backspace", a.modelState.filter)
	}

	_, _ = a.handleModelKey(tea.KeyMsg{Type: tea.KeyEsc})
	if a.modelState.filtering || a.modelState.filter != "" {
		t.Fatalf("esc should clear filter mode, got filtering=%v filter=%q", a.modelState.filtering, a.modelState.filter)
	}
}

func TestModelPickerEmptyCatalogEnterCloses(t *testing.T) {
	a := newModelScreen(t, t.TempDir())
	a.cfg.Provider = "huggingface" // no static catalogue
	a.modelState.picking = true
	a.modelState.pickingRole = roleAgent

	_, _ = a.handleModelKey(tea.KeyMsg{Type: tea.KeyEnter})
	if a.modelState.picking {
		t.Fatal("picker should close on enter with an empty catalogue")
	}
}

func TestModelPickerUpDownWrap(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	// The wrap arithmetic counts the openai catalogue, not the default one.
	t.Setenv("SIGNET_PROVIDER", "openai")
	a := newModelScreen(t, t.TempDir())
	a.modelState.agentScope = "session"
	a.modelState.picking = true
	a.modelState.pickingRole = roleAgent
	n := len(a.catalogFor("openai"))
	if n == 0 {
		t.Fatal("openai catalogue is empty")
	}

	a.modelState.modelIdx = 0
	_, _ = a.handleModelKey(tea.KeyMsg{Type: tea.KeyUp})
	if a.modelState.modelIdx != n-1 {
		t.Fatalf("modelIdx = %d, want %d after up-wrap", a.modelState.modelIdx, n-1)
	}
	_, _ = a.handleModelKey(tea.KeyMsg{Type: tea.KeyDown})
	if a.modelState.modelIdx != 0 {
		t.Fatalf("modelIdx = %d, want 0 after down-wrap", a.modelState.modelIdx)
	}
}

func TestModelPickerFilterSpaceAndEnter(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	a := newModelScreen(t, t.TempDir())
	a.modelState.agentScope = "session"
	a.modelState.picking = true
	a.modelState.pickingRole = roleAgent

	// Space in filter mode appends a space; enter leaves filter mode without
	// committing a selection.
	a.modelState.filtering = true
	a.modelState.filter = ""
	_, _ = a.handleModelKey(tea.KeyMsg{Type: tea.KeySpace})
	if a.modelState.filter != " " {
		t.Fatalf("filter = %q, want a single space", a.modelState.filter)
	}
	_, _ = a.handleModelKey(tea.KeyMsg{Type: tea.KeyEnter})
	if a.modelState.filtering {
		t.Fatal("enter should leave filter mode")
	}

	// Space outside filter mode selects, like enter.
	a.modelState.filter = ""
	a.modelState.modelIdx = 0
	_, _ = a.handleModelKey(tea.KeyMsg{Type: tea.KeySpace})
	if a.modelState.picking {
		t.Fatal("space should select and close the picker")
	}
}

func TestFilterModels(t *testing.T) {
	cat := []models.Model{{ID: "gpt-5"}, {ID: "gpt-5-mini"}, {ID: "claude-opus-4-5"}}

	if got := filterModels(cat, ""); len(got) != 3 {
		t.Fatalf("empty filter returned %d entries, want 3", len(got))
	}
	got := filterModels(cat, "GPT")
	if len(got) != 2 {
		t.Fatalf("case-insensitive filter returned %d entries, want 2", len(got))
	}
	got = filterModels(cat, "claude")
	if len(got) != 1 || got[0].ID != "claude-opus-4-5" {
		t.Fatalf("filter returned %+v, want claude-opus-4-5", got)
	}
}

func TestTrimLastRune(t *testing.T) {
	cases := map[string]string{
		"":      "",
		"a":     "",
		"abc":   "ab",
		"héllo": "héll",
		"日本語":   "日本",
	}
	for in, want := range cases {
		if got := trimLastRune(in); got != want {
			t.Errorf("trimLastRune(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHumanizeBytes(t *testing.T) {
	cases := map[int]string{
		512:         "512 B",
		2048:        "2 KiB",
		3 * 1048576: "3 MiB",
	}
	for n, want := range cases {
		if got := humanizeBytes(n); got != want {
			t.Errorf("humanizeBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestSafeRowBounds(t *testing.T) {
	rows := []modelRow{{roleAgent, settingsRow{key: "provider"}}}
	if got := safeRow(rows, 0); got.key != "provider" {
		t.Fatalf("safeRow(0).key = %q, want provider", got.key)
	}
	if got := safeRow(rows, -1); got.key != "" {
		t.Fatalf("safeRow(-1).key = %q, want empty", got.key)
	}
	if got := safeRow(rows, 1); got.key != "" {
		t.Fatalf("safeRow(1).key = %q, want empty", got.key)
	}
}

func TestScopeTarget(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := New(Options{Workdir: "/tmp/w"})

	if got := a.scopeTarget("session"); got != "(session only)" {
		t.Fatalf("scopeTarget(session) = %q", got)
	}
	got := a.scopeTarget("global")
	if !strings.HasSuffix(got, "settings.json") {
		t.Fatalf("scopeTarget(global) = %q, want settings path", got)
	}
	if want := config.ProjectSettingsPath("/tmp/w"); a.scopeTarget("project") != want || a.scopeTarget("bogus") != want {
		t.Fatalf("project/bogus scope should fall back to %q, got %q/%q", want, a.scopeTarget("project"), a.scopeTarget("bogus"))
	}
}

func TestWindowStart(t *testing.T) {
	cases := []struct {
		off, cursor, n, rows, want int
	}{
		{0, 5, 3, 10, 0},    // everything fits
		{10, 5, 20, 5, 5},   // cursor above the window
		{0, 15, 20, 5, 11},  // cursor below the window
		{18, 19, 20, 5, 15}, // clamp to n-rows
	}
	for _, c := range cases {
		if got := windowStart(c.off, c.cursor, c.n, c.rows); got != c.want {
			t.Errorf("windowStart(%d,%d,%d,%d) = %d, want %d", c.off, c.cursor, c.n, c.rows, got, c.want)
		}
	}
}

func TestClampIdx(t *testing.T) {
	cases := []struct{ idx, n, want int }{
		{2, 5, 2},
		{-1, 5, 0},
		{9, 5, 0},
		{3, 0, 0},
	}
	for _, c := range cases {
		if got := clampIdx(c.idx, c.n); got != c.want {
			t.Errorf("clampIdx(%d,%d) = %d, want %d", c.idx, c.n, got, c.want)
		}
	}
}

func TestModelEffortsUsesCatalog(t *testing.T) {
	a := New(Options{})
	a.catalogCache = map[string][]models.Model{
		"openai": {{ID: "gpt-5", Efforts: []string{"low", "high"}}},
	}
	a.cfg.Provider = "openai"
	a.cfg.Model = "gpt-5"
	a.settings.Classifier = nil

	if got := a.agentEffortOpts(); !slices.Equal(got, []string{"low", "high"}) {
		t.Fatalf("agentEffortOpts = %v, want [low high]", got)
	}
	if got := a.classifierEffortOpts(); !slices.Equal(got, []string{"low", "high"}) {
		t.Fatalf("classifierEffortOpts = %v, want [low high]", got)
	}
}

func TestModelSearchLineRenders(t *testing.T) {
	a := New(Options{})

	a.modelState.filter = ""
	a.modelState.filtering = false
	if !strings.Contains(a.modelSearchLine(), "/ to filter") {
		t.Fatalf("idle search line = %q, want / to filter hint", a.modelSearchLine())
	}

	a.modelState.filter = "gpt"
	if !strings.Contains(a.modelSearchLine(), "gpt") || !strings.Contains(a.modelSearchLine(), "esc clears") {
		t.Fatalf("filtered search line = %q, want filter and esc clears", a.modelSearchLine())
	}

	a.modelState.filtering = true
	if !strings.Contains(a.modelSearchLine(), "gpt▌") {
		t.Fatalf("focused search line = %q, want gpt block cursor", a.modelSearchLine())
	}
}

func TestModelPickerRenders(t *testing.T) {
	a := newModelScreen(t, t.TempDir())
	a.modelState.picking = true
	a.modelState.pickingRole = roleAgent

	v := a.modelView()
	if !strings.Contains(v, "model for") {
		t.Fatalf("picker view missing provider chip: %q", v)
	}
	if !strings.Contains(v, run.DefaultModel(a.cfg.Provider)) {
		t.Fatalf("picker view missing catalogue entries: %q", v)
	}
}

func TestCatalogTargetGatewayFallsBackToWorkersAI(t *testing.T) {
	t.Setenv("CLOUDFLARE_API_KEY", "cf-key")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct")
	src := run.EnvSource(os.Getenv)
	target := catalogTarget("cloudflare-ai-gateway", src)
	if target.Name != "cloudflare-workers-ai" {
		t.Fatalf("target name = %q, want cloudflare-workers-ai", target.Name)
	}
}

func TestModelKeyEnterOpensClassifierPicker(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()
	if err := config.Mutate(config.ScopeProject, workdir, func(s *config.Settings) error {
		s.Classifier = &config.ClassifierSettings{Provider: "openai", Model: "gpt-5"}
		return nil
	}); err != nil {
		t.Fatalf("seed classifier: %v", err)
	}
	a := newModelScreen(t, workdir)
	if err := a.reloadSettings(); err != nil {
		t.Fatalf("reload settings: %v", err)
	}
	a.modelState.classifierScope = "project"
	a.modelState.selected = 5 // classifier model row

	_, _ = a.handleModelKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !a.modelState.picking || a.modelState.pickingRole != roleClassifier {
		t.Fatalf("picking=%v role=%q, want classifier picker open", a.modelState.picking, a.modelState.pickingRole)
	}
	// Rendering the picker resolves the classifier catalogue path.
	v := a.modelView()
	if !strings.Contains(v, "model for") || !strings.Contains(v, "openai") {
		t.Fatalf("classifier picker render missing provider chip: %q", v)
	}
}

func TestModelKeyTogglesClassifierCaveman(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := newModelScreen(t, t.TempDir())
	a.modelState.classifierScope = "project"
	a.modelState.selected = 8 // classifier caveman row

	_, _ = a.handleModelKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !a.settings.ClassifierCavemanEnabled() {
		t.Fatal("classifier caveman should be toggled on")
	}
}

func TestModelChangeModelRowDisabledNoop(t *testing.T) {
	a := newModelScreen(t, t.TempDir())
	a.modelState.rows = a.modelRows()
	a.modelState.selected = 7 // classifier effort row, disabled when reasoning is off

	if cmd := a.changeModelRow(); cmd != nil {
		t.Fatal("changeModelRow on a disabled row must be a no-op")
	}
}

func TestModelKeyUnsetAgentModelRow(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	a := newModelScreen(t, t.TempDir())
	a.modelState.agentScope = "session"
	a.cfg.Model = "gpt-4.1"

	a.modelState.selected = 1 // agent model row
	_, _ = a.handleModelKey(modelKey("x"))
	if a.cfg.Model != run.DefaultModel(a.cfg.Provider) {
		t.Fatalf("model = %q, want the %s default after unset", a.cfg.Model, a.cfg.Provider)
	}
}

func TestModelKeyUnsetClassifierModelRow(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()
	if err := config.Mutate(config.ScopeProject, workdir, func(s *config.Settings) error {
		s.Classifier = &config.ClassifierSettings{Provider: "openai", Model: "gpt-4"}
		return nil
	}); err != nil {
		t.Fatalf("seed classifier: %v", err)
	}
	a := newModelScreen(t, workdir)
	if err := a.reloadSettings(); err != nil {
		t.Fatalf("reload settings: %v", err)
	}
	a.modelState.classifierScope = "project"
	a.modelState.selected = 5 // classifier model row

	_, _ = a.handleModelKey(modelKey("x"))
	if a.settings.Classifier.Model != "" {
		t.Fatalf("classifier model = %q, want empty after unset", a.settings.Classifier.Model)
	}
}

func TestCycleAgentEffortEmptyOptsNoop(t *testing.T) {
	a := New(Options{})
	if cmd := a.cycleAgentEffort(nil); cmd != nil {
		t.Fatal("cycleAgentEffort with no options must be a no-op")
	}
}

func TestApplyClassifierChangeSurfacesUnconfigured(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	a.cfg.Provider = "openai"
	a.settings.Classifier = &config.ClassifierSettings{Provider: "anthropic", Model: "claude-opus-4-5"}

	_ = a.applyClassifierChange()
	if a.modelState.errorMsg == "" {
		t.Fatal("expected an unconfigured classifier provider to surface an error")
	}
}

func TestAgentEffortOptsDefaultFallback(t *testing.T) {
	a := New(Options{})
	a.cfg.Provider = "huggingface" // no static catalogue
	a.cfg.Model = "any"
	if got := a.agentEffortOpts(); !slices.Equal(got, defaultModelEfforts) {
		t.Fatalf("agentEffortOpts = %v, want default efforts %v", got, defaultModelEfforts)
	}
	if got := a.classifierEffortOpts(); !slices.Equal(got, defaultModelEfforts) {
		t.Fatalf("classifierEffortOpts = %v, want default efforts %v", got, defaultModelEfforts)
	}
}
