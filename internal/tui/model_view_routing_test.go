package tui

import (
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/models"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/rolemanager/jev"
	"github.com/vulnetix/belai/internal/run"
)

// TestModelPickerRoutingLeavesOutJev pins that routed use-case pickers never
// offer a Jev Decisions model: routed use cases need chat, and Jev cannot
// chat. The classifier picker still offers it (see
// TestModelPickerClassifierStillOffersJev).
func TestModelPickerRoutingLeavesOutJev(t *testing.T) {
	a := modelScreen(t)
	a.settings.Routing = &config.RoutingSettings{
		Kind: config.RoutingRouted,
		UseCases: map[string]config.RoutingTarget{
			rolemanager.UseCaseModeEval: {Provider: "openrouter", Model: "openai/gpt-5"},
		},
	}
	a.catalogCache = map[string][]models.Model{
		"openrouter": {
			{ID: "typesafe/jev-1.13"},
			{ID: "typesafe/jev-1.13-20260917"},
			{ID: "openai/gpt-5"},
			{ID: "anthropic/claude-3.5-sonnet"},
		},
	}
	a.modelState.picking = true
	a.modelState.pickingRole = roleRouting
	a.modelState.routingUseCase = rolemanager.UseCaseModeEval

	name, catalog := a.modelPickerCatalog()
	if name != "openrouter" {
		t.Fatalf("picker name = %q, want openrouter", name)
	}
	if len(catalog) == 0 {
		t.Fatal("routing catalogue is empty")
	}
	for _, m := range catalog {
		if jev.IsDecisionsModel(name, m.ID) {
			t.Fatalf("routing picker must leave out Jev Decisions models, got %q", m.ID)
		}
	}
}

// TestModelPickerClassifierStillOffersJev pins that the classifier picker
// keeps offering the Jev Decisions model on the models path even though the
// routing picker drops it.
func TestModelPickerClassifierStillOffersJev(t *testing.T) {
	a := modelScreen(t)
	a.settings.Classifier = &config.ClassifierSettings{Kind: "models", Provider: "openrouter", Model: "typesafe/jev-1.13"}
	a.catalogCache = map[string][]models.Model{
		"openrouter": {
			{ID: "openai/gpt-5"},
		},
	}
	a.modelState.picking = true
	a.modelState.pickingRole = roleClassifier

	name, catalog := a.modelPickerCatalog()
	if name != "openrouter" {
		t.Fatalf("picker name = %q, want openrouter", name)
	}
	found := false
	for _, m := range catalog {
		if m.ID == jev.DefaultModel {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("classifier picker must still offer the Jev Decisions model: %v", catalog)
	}
}

// TestRoutedModelCount pins the footer's "n models active" count: zero unless
// the router is engaged, and distinct provider/model pairs (main included)
// otherwise.
func TestRoutedModelCount(t *testing.T) {
	main := run.Config{Provider: "openai", Model: "gpt-5"}
	if n := routedModelCount(main); n != 0 {
		t.Fatalf("defined routing: got %d, want 0", n)
	}
	empty := main
	empty.Routing = run.RoutingConfig{Kind: config.RoutingRouted}
	if n := routedModelCount(empty); n != 0 {
		t.Fatalf("routed with empty pool: got %d, want 0", n)
	}
	routed := main
	routed.Routing = run.RoutingConfig{Kind: config.RoutingRouted, Candidates: []run.RoutingCandidate{
		{Key: "main", Cfg: run.Config{Provider: "openai", Model: "gpt-5"}},
		{Key: "clarify", Cfg: run.Config{Provider: "anthropic", Model: "claude-sonnet-5"}},
		{Key: "compaction", Cfg: run.Config{Provider: "anthropic", Model: "claude-sonnet-5"}},
		{Key: "session_name", Cfg: run.Config{Provider: "anthropic", Model: "claude-haiku-4-5"}},
	}}
	if n := routedModelCount(routed); n != 3 {
		t.Fatalf("routed pool: got %d, want 3", n)
	}
}
