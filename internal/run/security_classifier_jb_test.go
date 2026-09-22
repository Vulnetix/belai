//go:build signet_bert_jailbreak

package run

import (
	"testing"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/mlclassify"
)

// TestResolveSecurityClassifierPhase2EmbeddedDefaultThreshold pins the
// jailbreak-variant default: the embedded phase-2 model is enabled, but its
// attack threshold starts at the raised 0.75 default rather than 0.5. The
// threshold is user-adjustable per phase and must round-trip through
// resolution.
func TestResolveSecurityClassifierPhase2EmbeddedDefaultThreshold(t *testing.T) {
	sc := ResolveSecurityClassifier(&config.ClassifierSettings{Kind: "models"})
	if sc.Kind != "models" {
		t.Fatalf("Kind = %q, want models", sc.Kind)
	}
	if sc.Phase1 == nil {
		t.Fatal("phase1 must be configured: it is the always-on saturation gate")
	}
	if sc.Phase2 == nil {
		t.Fatal("phase2 must be enabled by default on the jailbreak variant")
	}
	if sc.Phase2.ID != "jackhhao/jailbreak-classifier" ||
		sc.Phase2.Source != mlclassify.SourceEmbedded {
		t.Fatalf("phase2 = %+v, want the embedded jailbreak model", sc.Phase2)
	}
	if got := sc.Phase2.ThresholdOr(); got != mlclassify.DefaultThreshold {
		t.Fatalf("phase2 default threshold = %.2f, want %.2f", got, mlclassify.DefaultThreshold)
	}

	overridden := ResolveSecurityClassifier(&config.ClassifierSettings{
		Kind:   "models",
		Phase2: config.ClassifierPhaseSettings{Threshold: 0.85},
	})
	if overridden.Phase2 == nil {
		t.Fatal("phase2 must stay enabled when only a threshold is set")
	}
	if got := overridden.Phase2.ThresholdOr(); got != 0.85 {
		t.Fatalf("phase2 threshold = %.2f, want 0.85", got)
	}
}
