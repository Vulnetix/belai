package tui

import (
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/wire"
)

func saveProvidersForTest(t *testing.T, providers map[string]config.ProviderProfile) {
	t.Helper()
	t.Setenv("SIGNET_HOME", t.TempDir())
	if err := config.SaveGlobal(config.Settings{Providers: providers}); err != nil {
		t.Fatalf("SaveGlobal: %v", err)
	}
}

func TestModelViewListsCustomProviderAfterBuiltins(t *testing.T) {
	saveProvidersForTest(t, map[string]config.ProviderProfile{
		"my-llm": {BaseURL: "https://llm.example/v1", API: wire.SurfaceOpenAIChat},
	})
	a := New(Options{})
	names := a.providerNames()
	openaiIdx := indexOfString(names, "openai")
	customIdx := indexOfString(names, "my-llm")
	if openaiIdx < 0 || customIdx < 0 {
		t.Fatalf("expected both built-in and custom names, got %v", names)
	}
	if customIdx < openaiIdx {
		t.Fatalf("custom provider should sort after built-ins, got %v", names)
	}
}

func TestModelViewCustomProviderModels(t *testing.T) {
	saveProvidersForTest(t, map[string]config.ProviderProfile{
		"my-llm": {
			BaseURL: "https://llm.example/v1",
			API:     wire.SurfaceOpenAIChat,
			Models: []config.ProviderModel{
				{ID: "m1", Name: "Model One"},
				{ID: "m2"},
			},
		},
	})
	a := New(Options{})
	a.modelState = modelViewState{providerIdx: indexOfString(a.providerNames(), "my-llm")}
	view := a.modelView()
	if !strings.Contains(view, "m1") || !strings.Contains(view, "m2") {
		t.Fatalf("custom models not listed: %q", view)
	}
}

func TestModelViewEffortDisabledForCustom(t *testing.T) {
	saveProvidersForTest(t, map[string]config.ProviderProfile{
		"my-llm": {
			BaseURL: "https://llm.example/v1",
			API:     wire.SurfaceOpenAIChat,
			Models:  []config.ProviderModel{{ID: "m1"}},
		},
	})
	a := New(Options{})
	a.modelState = modelViewState{providerIdx: indexOfString(a.providerNames(), "my-llm")}
	view := a.modelView()
	if !strings.Contains(view, "unavailable") {
		t.Fatalf("effort should render unavailable for custom providers: %q", view)
	}
}

func TestCredentialViewListsCustomProvider(t *testing.T) {
	saveProvidersForTest(t, map[string]config.ProviderProfile{
		"my-llm": {BaseURL: "https://llm.example/v1", API: wire.SurfaceOpenAIChat},
	})
	a := New(Options{})
	view := a.credentialView()
	if !strings.Contains(view, "my-llm") {
		t.Fatalf("credential view should list custom provider: %q", view)
	}
}
