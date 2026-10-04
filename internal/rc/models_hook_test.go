package rc

import (
	"testing"

	"github.com/vulnetix/belai/internal/config"
)

func advertised(t *testing.T, configured []string) []string {
	t.Helper()
	var names []string
	for _, p := range buildModels(config.Settings{}, noState, noEnv, configured).Providers {
		names = append(names, p.Name)
	}
	return names
}

// The hideProvider hook is nil in the default build: every configured
// provider is advertised, in sorted order.
func TestBuildModelsProviderFilterHook(t *testing.T) {
	saved := hideProvider
	t.Cleanup(func() { hideProvider = saved })
	configured := []string{"openai", "cloudflare-workers-ai", "anthropic"}

	hideProvider = nil
	got := advertised(t, configured)
	if len(got) != 3 || got[0] != "anthropic" || got[1] != "cloudflare-workers-ai" || got[2] != "openai" {
		t.Fatalf("without a hook providers = %v", got)
	}

	hideProvider = func(name string) bool { return name == "anthropic" }
	got = advertised(t, configured)
	if len(got) != 2 || got[0] != "cloudflare-workers-ai" || got[1] != "openai" {
		t.Fatalf("with a hook providers = %v", got)
	}
}
