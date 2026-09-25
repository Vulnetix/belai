package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/mlclassify"
	"github.com/vulnetix/signet/internal/models"
	"github.com/vulnetix/signet/internal/rolemanager/jev"
)

func modelScreen(t *testing.T) *App {
	t.Helper()
	a := New(Options{})
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	_ = a.enterModel()
	return a
}

func rowByKey(rows []modelRow, key string) (modelRow, bool) {
	for _, r := range rows {
		if r.key == key {
			return r, true
		}
	}
	return modelRow{}, false
}

func TestModelRowsPhasesHiddenForLLM(t *testing.T) {
	a := modelScreen(t)
	a.settings.Classifier = &config.ClassifierSettings{Kind: "llm"}
	for _, r := range a.modelRows() {
		if r.key == "phase1" || r.key == "phase2" || r.key == "phase3" {
			t.Fatalf("phase row %q must be hidden when kind == llm", r.key)
		}
	}
	if _, ok := rowByKey(a.modelRows(), "kind"); !ok {
		t.Fatal("kind row must be present when kind == llm")
	}
}

func TestModelRowsPhasesForModelsVanilla(t *testing.T) {
	if mlclassify.Embedded() {
		t.Skip("covers the no-classifier binary; an embedded build runs its own phase-1 model")
	}
	t.Setenv("HF_TOKEN", "")
	t.Setenv("HUGGINGFACE_TOKEN", "")
	a := modelScreen(t)
	a.settings.Classifier = &config.ClassifierSettings{Kind: "models"}

	p1, ok := rowByKey(a.modelRows(), "phase1")
	if !ok {
		t.Fatal("phase1 row missing for models kind")
	}
	if !strings.Contains(p1.value, "off — no model in this build") || !p1.disabled {
		t.Fatalf("phase1 row = %+v, want locked 'off — no model in this build'", p1)
	}

	p2, ok := rowByKey(a.modelRows(), "phase2")
	if !ok {
		t.Fatal("phase2 row missing for models kind")
	}
	if !strings.Contains(p2.value, "deferred to phase 3") || !p2.disabled {
		t.Fatalf("phase2 row = %+v, want locked 'deferred to phase 3' status", p2)
	}

	p3, ok := rowByKey(a.modelRows(), "phase3")
	if !ok {
		t.Fatal("phase3 row missing for models kind")
	}
	if !p3.disabled || !strings.Contains(p3.value, "off: set classifier provider + model to enable") {
		t.Fatalf("phase3 row = %+v, want locked off status", p3)
	}
}

func TestModelRowsPhase2ExplicitDisabled(t *testing.T) {
	t.Setenv("HF_TOKEN", "")
	t.Setenv("HUGGINGFACE_TOKEN", "")
	a := modelScreen(t)
	a.settings.Classifier = &config.ClassifierSettings{
		Kind:   "models",
		Phase2: config.ClassifierPhaseSettings{Source: "disabled"},
	}

	p2, ok := rowByKey(a.modelRows(), "phase2")
	if !ok {
		t.Fatal("phase2 row missing for models kind")
	}
	if !strings.Contains(p2.value, "disabled") || p2.disabled {
		t.Fatalf("phase2 row = %+v, want editable 'disabled' status", p2)
	}
}

func TestModelPhase3RowOn(t *testing.T) {
	a := modelScreen(t)
	a.settings.Classifier = &config.ClassifierSettings{Kind: "models", Provider: "openai", Model: "gpt-5"}
	row := a.classifierPhase3Row()
	// Phase 3 takes jailbreak too only when no local jailbreak gate runs; the
	// jailbreak variant embeds one.
	scope := "injection + extraction"
	if a.resolvedSecurityClassifier().Phase2Deferred {
		scope = "injection + jailbreak + extraction"
	}
	if !row.disabled || !strings.Contains(row.value, scope+" · openai/gpt-5") {
		t.Fatalf("phase3 row = %+v, want locked '%s · openai/gpt-5'", row, scope)
	}
}

func TestModelKindRowEditableOnVanilla(t *testing.T) {
	a := modelScreen(t)
	a.settings.Classifier = &config.ClassifierSettings{Kind: "llm"}
	kind, ok := rowByKey(a.modelRows(), "kind")
	if !ok {
		t.Fatal("kind row missing")
	}
	if kind.disabled {
		t.Fatal("kind row must be editable on a vanilla binary")
	}
	if kind.value != "llm" {
		t.Fatalf("kind value = %q, want llm", kind.value)
	}
}

func TestCycleClassifierKind(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := newModelScreen(t, t.TempDir())
	a.modelState.classifierScope = "project"
	a.settings.Classifier = &config.ClassifierSettings{Kind: "llm"}
	a.modelState.rows = a.modelRows()

	_ = a.cycleClassifierKind([]string{"llm", "models"})
	if a.settings.Classifier.Kind != "models" {
		t.Fatalf("kind = %q, want models after cycle", a.settings.Classifier.Kind)
	}
	_ = a.cycleClassifierKind([]string{"llm", "models"})
	if a.settings.Classifier.Kind != "llm" {
		t.Fatalf("kind = %q, want llm after second cycle", a.settings.Classifier.Kind)
	}
}

func hasStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func TestClassifierCatalogForHuggingFaceOnlyCuratedBERT(t *testing.T) {
	a := modelScreen(t)
	catalog := []models.Model{
		{ID: "GuardrailsAI/prompt-saturation-attack-detector"},
		{ID: "leomaurodesenv/bert-base-uncased-trustairlab-jailbreak"},
		{ID: "openai/gpt-4o"},
		{ID: "leomaurodesenv/bert-base-uncased-jailbreakv-28k"},
		{ID: "hurtmongoose/bert-base-detect-jailbreak"},
		{ID: "hurtmongoose/jailbreak-bert-base-uncased"},
		{ID: "meta-llama/llama-3-70b"},
	}
	got := a.classifierCatalogFor("huggingface", catalog)
	if len(got) != 5 {
		t.Fatalf("huggingface classifier catalogue = %v, want exactly the five curated BERT ids", got)
	}
	for _, m := range got {
		if !mlclassify.IsKnownClassifierModel(m.ID) {
			t.Fatalf("huggingface classifier catalogue leaked %q", m.ID)
		}
	}
}

func TestClassifierCatalogForOpenRouterOnlyJev(t *testing.T) {
	a := modelScreen(t)
	catalog := []models.Model{
		{ID: "typesafe/jev-1"},
		{ID: "typesafe/jev-1.5"},
		{ID: "openai/gpt-4o"},
		{ID: "anthropic/claude-3.5-sonnet"},
		{ID: "typesafe/jev-2"},
	}
	got := a.classifierCatalogFor("openrouter", catalog)
	// The known Jev Decisions model is seeded first; the three typesafe/jev*
	// catalogue entries follow; general-chat models are excluded.
	if len(got) != 4 || got[0].ID != jev.DefaultModel {
		t.Fatalf("openrouter classifier catalogue = %v, want the seeded Jev model plus three typesafe/jev* entries", got)
	}
	for _, m := range got {
		if !strings.HasPrefix(m.ID, "typesafe/jev") {
			t.Fatalf("openrouter classifier catalogue leaked %q", m.ID)
		}
	}
}

func TestClassifierCatalogForBroadProviderUnfiltered(t *testing.T) {
	a := modelScreen(t)
	catalog := []models.Model{
		{ID: "qwen2.5-7b-instruct-q4_k_m"},
		{ID: "llama3"},
		{ID: "typesafe/jev-1"},
	}
	for _, provider := range []string{"ollama", "llama-server", "my-custom"} {
		got := a.classifierCatalogFor(provider, catalog)
		if len(got) != len(catalog) {
			t.Fatalf("%s classifier catalogue = %v, want unfiltered", provider, got)
		}
	}
}

func TestClassifierPhaseOptsDisabledOnlyWhenEnabled(t *testing.T) {
	t.Setenv("HF_TOKEN", "hf-x")
	t.Setenv("HUGGINGFACE_TOKEN", "")
	a := modelScreen(t)

	// Deferred phase 2 (no local jailbreak model) must not offer "disabled".
	a.settings.Classifier = &config.ClassifierSettings{Kind: "models"}
	if hasStr(a.classifierPhaseOpts(2), "disabled") {
		t.Fatal("deferred phase 2 must not offer a 'disabled' cycle stop")
	}

	// An enabled remote jailbreak gate offers "disabled" as its turn-off stop.
	a.settings.Classifier = &config.ClassifierSettings{
		Kind: "models",
		Phase2: config.ClassifierPhaseSettings{
			Model:  "leomaurodesenv/bert-base-uncased-trustairlab-jailbreak",
			Source: "huggingface",
		},
	}
	if !hasStr(a.classifierPhaseOpts(2), "disabled") {
		t.Fatal("enabled phase 2 must offer a 'disabled' turn-off stop")
	}
}

func TestClassifierProviderList(t *testing.T) {
	t.Setenv("HF_TOKEN", "")
	t.Setenv("HUGGINGFACE_TOKEN", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	a := modelScreen(t)

	got := a.classifierProviders()
	if hasStr(got, "huggingface") {
		t.Fatal("huggingface must not appear for the classifier role without an HF token")
	}
	if hasStr(got, "openrouter") {
		t.Fatal("openrouter must not appear for the classifier role without OPENROUTER_API_KEY")
	}
	if hasStr(got, "openai") || hasStr(got, "anthropic") {
		t.Fatalf("general-chat providers must not appear for the classifier role: %v", got)
	}
	if !hasStr(got, "llama-server") || !hasStr(got, "ollama") {
		t.Fatalf("built-in local servers must appear for the classifier role: %v", got)
	}

	// An HF token makes huggingface appear for the classifier role only.
	t.Setenv("HF_TOKEN", "hf-x")
	got = a.classifierProviders()
	if !hasStr(got, "huggingface") {
		t.Fatal("huggingface must appear for the classifier role when HF_TOKEN is set")
	}

	// OPENROUTER_API_KEY alone is enough: the picker filters to typesafe/jev*
	// after the provider is chosen, so the gate is the key, not a pre-fetched
	// catalogue.
	t.Setenv("HF_TOKEN", "")
	t.Setenv("OPENROUTER_API_KEY", "or-key")
	a = modelScreen(t)
	if !hasStr(a.classifierProviders(), "openrouter") {
		t.Fatal("openrouter must appear for the classifier role when OPENROUTER_API_KEY is set")
	}
}

func TestModelRowsPhasesNoClassifier(t *testing.T) {
	if mlclassify.Embedded() {
		t.Skip("covers the no-classifier binary; an embedded build runs its own phase-1 model")
	}
	t.Setenv("HF_TOKEN", "")
	t.Setenv("HUGGINGFACE_TOKEN", "")
	a := modelScreen(t)
	a.settings.Classifier = &config.ClassifierSettings{Kind: "models"}

	kind, ok := rowByKey(a.modelRows(), "kind")
	if !ok {
		t.Fatal("kind row missing")
	}
	if kind.disabled {
		t.Fatal("kind row must be editable on a no-classifier binary")
	}

	p1, ok := rowByKey(a.modelRows(), "phase1")
	if !ok {
		t.Fatal("phase1 row missing")
	}
	if !strings.Contains(p1.value, "off — no model in this build") || !p1.disabled {
		t.Fatalf("phase1 row = %+v, want locked 'off — no model in this build'", p1)
	}

	p2, ok := rowByKey(a.modelRows(), "phase2")
	if !ok {
		t.Fatal("phase2 row missing")
	}
	if !strings.Contains(p2.value, "deferred to phase 3") || !p2.disabled {
		t.Fatalf("phase2 row = %+v, want locked 'deferred to phase 3'", p2)
	}
}

func TestModelRowsPhasesNoClassifierHFRemote(t *testing.T) {
	t.Setenv("HF_TOKEN", "hf-x")
	t.Setenv("HUGGINGFACE_TOKEN", "")
	a := modelScreen(t)
	a.settings.Classifier = &config.ClassifierSettings{
		Kind:   "models",
		Phase1: config.ClassifierPhaseSettings{Model: "GuardrailsAI/prompt-saturation-attack-detector", Source: "huggingface"},
	}

	p1, ok := rowByKey(a.modelRows(), "phase1")
	if !ok {
		t.Fatal("phase1 row missing")
	}
	if !strings.Contains(p1.value, "GuardrailsAI/prompt-saturation-attack-detector") ||
		!strings.Contains(p1.value, "huggingface") {
		t.Fatalf("phase1 row = %+v, want remote huggingface model", p1)
	}
}

func TestClassifierProviderListOpenRouterWithResolver(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	t.Setenv("OPENROUTER_API_KEY", "or-key")
	a := newModelScreen(t, t.TempDir())
	a.resolver = newTestResolver(t, a.workdir)
	got := a.classifierProviders()
	if !hasStr(got, "openrouter") {
		t.Fatalf("openrouter must appear for the classifier role when configured via the resolver, got %v", got)
	}
}

func TestModelRowsPhase1NoHFDefaultWithToken(t *testing.T) {
	if mlclassify.Embedded() {
		t.Skip("the no-classifier binary's phase-1 row is covered here; an embedded build runs its own phase-1 model")
	}
	t.Setenv("HF_TOKEN", "hf-x")
	t.Setenv("HUGGINGFACE_TOKEN", "")
	a := modelScreen(t)
	a.settings.Classifier = &config.ClassifierSettings{Kind: "models"}

	p1, ok := rowByKey(a.modelRows(), "phase1")
	if !ok {
		t.Fatal("phase1 row missing")
	}
	// A HuggingFace token alone no longer fabricates a remote phase-1
	// default: HF serverless inference cannot serve the known saturation
	// model, so the row names the ways out instead.
	if !strings.Contains(p1.value, "off — no model in this build") || !p1.disabled {
		t.Fatalf("phase1 row = %+v, want locked 'off — no model in this build'", p1)
	}
}

func TestModelPickerLLMKindShowsFullCatalog(t *testing.T) {
	a := modelScreen(t)
	a.settings.Classifier = &config.ClassifierSettings{Kind: "llm", Provider: "openrouter"}
	a.modelState.picking = true
	a.modelState.pickingRole = roleClassifier

	name, catalog := a.modelPickerCatalog()
	if name != "openrouter" {
		t.Fatalf("picker name = %q, want openrouter", name)
	}
	// The llm-kind classifier is the LLM sentinel: the full chat catalogue must
	// be selectable, not the Jev-only filtered list.
	if len(catalog) < 2 {
		t.Fatalf("llm-kind classifier catalogue = %v, want the full openrouter chat catalogue", catalog)
	}
	for _, m := range catalog {
		if m.ID == jev.DefaultModel {
			t.Fatalf("llm-kind classifier catalogue must not be filtered to Jev: %v", catalog)
		}
	}
}

func TestModelPickerShowsCuratedModelBlurb(t *testing.T) {
	a := modelScreen(t)
	a.settings.Classifier = &config.ClassifierSettings{Kind: "models", Provider: "huggingface"}
	a.modelState.picking = true
	a.modelState.pickingRole = roleClassifier

	out := a.modelPicker()
	if !strings.Contains(out, "GuardrailsAI/prompt-saturation-attack-detector") {
		t.Fatalf("picker missing the curated BERT model id:\n%s", out)
	}
	if !strings.Contains(out, "prompt-saturation gate") {
		t.Fatalf("picker missing the curated model blurb:\n%s", out)
	}
}
