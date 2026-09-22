//go:build signet_bert_jailbreak

package run

import (
	"testing"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/mlclassify"
)

// TestResolveSecurityClassifierPhase2OptInWhenEmbedded pins the production
// default for the jailbreak gate. The jailbreak variant embeds the phase-2
// model, but the model over-triggers on ordinary tool results at any threshold,
// so it must not run unless the user explicitly sets phase2.source or
// phase2.model. Phase 1 stays on (the saturation gate is precise on tool
// output), and thresholds default to the raised 0.75.
func TestResolveSecurityClassifierPhase2OptInWhenEmbedded(t *testing.T) {
	sc := ResolveSecurityClassifier(&config.ClassifierSettings{Kind: "models"})
	if sc.Kind != "models" {
		t.Fatalf("Kind = %q, want models", sc.Kind)
	}
	if sc.Phase1 == nil {
		t.Fatal("phase1 must be configured: it is the always-on saturation gate")
	}
	if sc.Phase2 != nil {
		t.Fatalf("phase2 must default to disabled even when embedded, got %+v", sc.Phase2)
	}

	enabled := ResolveSecurityClassifier(&config.ClassifierSettings{
		Kind:   "models",
		Phase2: config.ClassifierPhaseSettings{Source: "embedded"},
	})
	if enabled.Phase2 == nil {
		t.Fatal("explicit phase2.source=embedded must enable the gate")
	}
	if enabled.Phase2.ID != "jackhhao/jailbreak-classifier" ||
		enabled.Phase2.Source != mlclassify.SourceEmbedded {
		t.Fatalf("phase2 = %+v, want the embedded jailbreak model", enabled.Phase2)
	}
	if got := enabled.Phase2.ThresholdOr(); got != mlclassify.DefaultThreshold {
		t.Fatalf("phase2 default threshold = %.2f, want %.2f", got, mlclassify.DefaultThreshold)
	}
}
