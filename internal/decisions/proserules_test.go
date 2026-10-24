package decisions

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/proserules"
)

// Tev1 and JevK5 were trained on their system text verbatim and are read
// from one letter's logprobs, so the prose rules every other system prompt
// carries must never reach them.
func TestDecisionPromptsCarryNoProseRules(t *testing.T) {
	first, _, _ := strings.Cut(proserules.Rules, "\n")
	for name, system := range map[string]string{"tev1": tev1System, "jevk5": jevk5System} {
		if strings.Contains(system, first) {
			t.Errorf("%s system prompt carries the prose rules", name)
		}
	}
}
