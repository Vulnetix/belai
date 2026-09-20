package tui

import (
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
