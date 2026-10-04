//go:build belai_sandbox

package rc

import (
	"testing"

	"github.com/vulnetix/belai/internal/config"
)

func TestSandboxPickerHidesStockWorkersAI(t *testing.T) {
	got := advertised(t, []string{"cloudflare-workers-ai", "builtin", "openai"})
	if len(got) != 2 || got[0] != "builtin" || got[1] != "openai" {
		t.Fatalf("providers = %v", got)
	}
	m := buildModels(config.Settings{}, noState, noEnv, []string{"builtin"})
	if len(m.Providers) != 1 {
		t.Fatalf("providers = %+v", m.Providers)
	}
	ids := m.Providers[0].Models
	if len(ids) != 2 || ids[0] != "pix-smart" || ids[1] != "pix-fast" {
		t.Fatalf("builtin models = %v", ids)
	}
}
