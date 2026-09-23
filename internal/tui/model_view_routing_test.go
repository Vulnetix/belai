package tui

import (
	"testing"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/models"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/rolemanager/jev"
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
