//go:build belai_bert && !belai_bert_jailbreak

package run

import (
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/mlclassify"
)

// TestResolveSecurityClassifierBertOnlyDeferred pins the BERT-only scenario:
// the variant embeds the phase-1 saturation gate but not the jailbreak gate,
// so an unset phase 2 is deferred to phase 3 (not disabled) and phase 1 stays
// embedded and always-on.
func TestResolveSecurityClassifierBertOnlyDeferred(t *testing.T) {
	sc := ResolveSecurityClassifier(&config.ClassifierSettings{Kind: "models"})
	if sc.Phase1 == nil || sc.Phase1.Source != mlclassify.SourceEmbedded {
		t.Fatalf("phase1 = %+v, want embedded saturation gate", sc.Phase1)
	}
	if sc.Phase2 != nil {
		t.Fatalf("phase2 = %+v, want nil", sc.Phase2)
	}
	if !sc.Phase2Deferred {
		t.Fatal("BERT-only binary must defer phase 2 to phase 3")
	}
}
