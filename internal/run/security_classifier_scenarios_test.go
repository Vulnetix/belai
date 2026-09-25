package run

import (
	"testing"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/mlclassify"
)

// phase2EmbeddedAvailable reports whether this build variant embeds the
// jailbreak model. Scenarios that differ between the no-classifier, BERT-only
// and jailbreak variants branch on it instead of assuming a tag.
func phase2EmbeddedAvailable() bool {
	_, ok := mlclassify.EmbeddedPhase2()
	return ok
}

// TestResolveSecurityClassifierScenarioNoClassifier pins the no-classifier
// (vanilla) scenario with no HuggingFace token: kind models resolves no phase-1
// model, and the jailbreak gate is deferred to phase 3 rather than silently
// downgraded to the LLM sentinel.
func TestResolveSecurityClassifierScenarioNoClassifier(t *testing.T) {
	t.Setenv("HF_TOKEN", "")
	t.Setenv("HUGGINGFACE_TOKEN", "")
	sc := ResolveSecurityClassifier(&config.ClassifierSettings{Kind: "models"})
	if sc.Kind != "models" {
		t.Fatalf("Kind = %q, want models", sc.Kind)
	}
	if !sc.Phase3On {
		t.Fatal("phase 3 must inherit the main model when provider/model are unset")
	}
	if sc.Phase2 != nil {
		t.Fatalf("phase2 = %+v, want nil", sc.Phase2)
	}
	if mlclassify.Embedded() {
		if sc.Phase1 == nil {
			t.Fatal("phase1 must be embedded and configured on an embedded build")
		}
	} else if sc.Phase1 != nil {
		t.Fatalf("phase1 = %+v, want nil on a no-classifier binary", sc.Phase1)
	}
	// Deferred on every variant without an embedded jailbreak model; on the
	// jailbreak variant the embedded gate is available-but-off, so an unset
	// phase 2 is "disabled", not deferred.
	if want := !phase2EmbeddedAvailable(); sc.Phase2Deferred != want {
		t.Fatalf("Phase2Deferred = %t, want %t", sc.Phase2Deferred, want)
	}
}

// TestResolveSecurityClassifierScenarioRemoteHF pins the remote HuggingFace
// scenario: explicit phase models resolve as remote gates with the curated
// catalogue attack labels, and a running jailbreak gate is never deferred.
func TestResolveSecurityClassifierScenarioRemoteHF(t *testing.T) {
	cls := &config.ClassifierSettings{
		Kind: "models",
		Phase1: config.ClassifierPhaseSettings{
			Model:  "GuardrailsAI/prompt-saturation-attack-detector",
			Source: "huggingface",
		},
		Phase2: config.ClassifierPhaseSettings{
			Model:  "leomaurodesenv/bert-base-uncased-trustairlab-jailbreak",
			Source: "huggingface",
		},
	}
	sc := ResolveSecurityClassifier(cls)
	if sc.Phase1 == nil || sc.Phase1.Source != mlclassify.SourceHuggingFace {
		t.Fatalf("phase1 = %+v, want remote huggingface gate", sc.Phase1)
	}
	if sc.Phase1.AttackLabel != "LABEL_1" {
		t.Fatalf("phase1 AttackLabel = %q, want LABEL_1", sc.Phase1.AttackLabel)
	}
	if sc.Phase2 == nil || sc.Phase2.Source != mlclassify.SourceHuggingFace {
		t.Fatalf("phase2 = %+v, want remote huggingface gate", sc.Phase2)
	}
	if sc.Phase2.AttackLabel != "unsafe" {
		t.Fatalf("phase2 AttackLabel = %q, want unsafe", sc.Phase2.AttackLabel)
	}
	if sc.Phase2Deferred {
		t.Fatal("a configured remote jailbreak gate must not be deferred")
	}
}

// TestResolveSecurityClassifierScenarioRemoteOpenRouterJev pins the remote
// OpenRouter Jev scenario: a classifier provider+model on the models path
// turns phase 3 on, and the jailbreak gate is deferred exactly when this
// variant cannot run it locally.
func TestResolveSecurityClassifierScenarioRemoteOpenRouterJev(t *testing.T) {
	cls := &config.ClassifierSettings{
		Kind:     "models",
		Provider: "openrouter",
		Model:    "typesafe/jev-1",
	}
	sc := ResolveSecurityClassifier(cls)
	if !sc.Phase3On {
		t.Fatal("phase 3 must be on when provider and model are both set")
	}
	if sc.Phase2 != nil {
		t.Fatalf("phase2 = %+v, want nil", sc.Phase2)
	}
	if want := !phase2EmbeddedAvailable(); sc.Phase2Deferred != want {
		t.Fatalf("Phase2Deferred = %t, want %t", sc.Phase2Deferred, want)
	}
}

// TestResolveSecurityClassifierExplicitDisabledNotDeferred pins that an
// explicit phase2.source "disabled" is a deliberate turn-off, never deferred
// to phase 3.
func TestResolveSecurityClassifierExplicitDisabledNotDeferred(t *testing.T) {
	sc := ResolveSecurityClassifier(&config.ClassifierSettings{
		Kind:   "models",
		Phase2: config.ClassifierPhaseSettings{Source: "disabled"},
	})
	if sc.Phase2 != nil {
		t.Fatalf("phase2 = %+v, want nil", sc.Phase2)
	}
	if sc.Phase2Deferred {
		t.Fatal("explicit phase2.source=disabled must not be deferred to phase 3")
	}
}

// TestResolveSecurityClassifierScenarioNoClassifierHF pins the no-classifier
// scenario with a HuggingFace token: a token alone must not resolve a phase-1
// model. The old default (the known saturation model over the inference API)
// failed every prompt, because HF serverless inference cannot serve that
// model — its repo ships no tokenizer files — so the token no longer
// fabricates a remote default.
func TestResolveSecurityClassifierScenarioNoClassifierHF(t *testing.T) {
	t.Setenv("HF_TOKEN", "hf-x")
	t.Setenv("HUGGINGFACE_TOKEN", "")
	sc := ResolveSecurityClassifier(&config.ClassifierSettings{Kind: "models"})
	if mlclassify.Embedded() {
		t.Skip("embedded build already has phase 1")
	}
	if sc.Phase1 != nil {
		t.Fatalf("phase1 = %+v, want nil: a token alone must not fabricate a remote default", sc.Phase1)
	}
	// An explicit model still resolves, so the remote path remains reachable
	// for models HF can actually serve.
	explicit := ResolveSecurityClassifier(&config.ClassifierSettings{
		Kind:   "models",
		Phase1: config.ClassifierPhaseSettings{Model: "GuardrailsAI/prompt-saturation-attack-detector", Source: "huggingface"},
	})
	if explicit.Phase1 == nil || explicit.Phase1.Source != mlclassify.SourceHuggingFace {
		t.Fatalf("explicit phase1 = %+v, want remote huggingface gate", explicit.Phase1)
	}
}
