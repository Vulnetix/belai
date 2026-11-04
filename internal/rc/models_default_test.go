//go:build !belai_sandbox

package rc

import (
	"testing"

	"github.com/vulnetix/belai/internal/config"
)

// The default build runs a web session with nothing configured on openai.
func TestDefaultBuildFallsBackToOpenAI(t *testing.T) {
	m := buildModels(config.Settings{}, noState, noEnv, nil)
	if m.Default.Provider != "openai" || m.Default.Model != "gpt-5" {
		t.Fatalf("default = %+v", m.Default)
	}
}
