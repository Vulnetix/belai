package tui

import (
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/mlclassify"
)

// variantHasEmbeddedPhase2 reports whether this build variant embeds the
// jailbreak model. Scenario assertions branch on it so the matrix runs on
// every variant.
func variantHasEmbeddedPhase2() bool {
	_, ok := mlclassify.EmbeddedPhase2()
	return ok
}

// TestModelViewScenarioNoClassifier pins the /model rows for a no-classifier
// binary that explicitly chooses kind models: the kind row is editable, phase 1
// explains the missing gate, and phase 2 is deferred to phase 3.
func TestModelViewScenarioNoClassifier(t *testing.T) {
	t.Setenv("HF_TOKEN", "")
	t.Setenv("HUGGINGFACE_TOKEN", "")
	a := modelScreen(t)
	a.settings.Classifier = &config.ClassifierSettings{Kind: "models"}
	rows := a.modelRows()

	kind, ok := rowByKey(rows, "kind")
	if !ok {
		t.Fatal("kind row missing")
	}
	if kind.disabled {
		t.Fatal("kind row must be editable on a no-classifier binary")
	}

	p1, ok := rowByKey(rows, "phase1")
	if !ok {
		t.Fatal("phase1 row missing")
	}
	if mlclassify.Embedded() {
		if !strings.Contains(p1.value, "GuardrailsAI/prompt-saturation-attack-detector") {
			t.Fatalf("phase1 row = %q, want the embedded saturation model", p1.value)
		}
	} else if !strings.Contains(p1.value, "LLM sentinel (no HuggingFace key)") {
		t.Fatalf("phase1 row = %q, want the no-HF-key hint", p1.value)
	}

	p2, ok := rowByKey(rows, "phase2")
	if !ok {
		t.Fatal("phase2 row missing")
	}
	if variantHasEmbeddedPhase2() {
		if !strings.Contains(p2.value, "disabled") {
			t.Fatalf("phase2 row = %q, want disabled (embedded gate available but off)", p2.value)
		}
	} else if !strings.Contains(p2.value, "deferred to phase 3") {
		t.Fatalf("phase2 row = %q, want deferred to phase 3", p2.value)
	}
}

// TestModelViewScenarioRemoteHF pins the /model phase rows for a remote
// HuggingFace configuration.
func TestModelViewScenarioRemoteHF(t *testing.T) {
	t.Setenv("HF_TOKEN", "hf-x")
	t.Setenv("HUGGINGFACE_TOKEN", "")
	a := modelScreen(t)
	a.settings.Classifier = &config.ClassifierSettings{
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
	rows := a.modelRows()

	p1, ok := rowByKey(rows, "phase1")
	if !ok {
		t.Fatal("phase1 row missing")
	}
	if !strings.Contains(p1.value, "GuardrailsAI/prompt-saturation-attack-detector via huggingface") {
		t.Fatalf("phase1 row = %q, want remote huggingface model", p1.value)
	}

	p2, ok := rowByKey(rows, "phase2")
	if !ok {
		t.Fatal("phase2 row missing")
	}
	if !strings.Contains(p2.value, "leomaurodesenv/bert-base-uncased-trustairlab-jailbreak via huggingface") {
		t.Fatalf("phase2 row = %q, want remote huggingface model", p2.value)
	}
}

// TestModelViewScenarioRemoteOpenRouterJev pins the /model phase-3 row for a
// remote OpenRouter Jev configuration.
func TestModelViewScenarioRemoteOpenRouterJev(t *testing.T) {
	t.Setenv("HF_TOKEN", "")
	t.Setenv("HUGGINGFACE_TOKEN", "")
	a := modelScreen(t)
	a.settings.Classifier = &config.ClassifierSettings{
		Kind:     "models",
		Provider: "openrouter",
		Model:    "typesafe/jev-1",
	}
	p3, ok := rowByKey(a.modelRows(), "phase3")
	if !ok {
		t.Fatal("phase3 row missing")
	}
	if !p3.disabled {
		t.Fatal("phase3 row must be locked (a derived status, not editable)")
	}
	if !strings.Contains(p3.value, "openrouter/typesafe/jev-1") {
		t.Fatalf("phase3 row = %q, want openrouter/typesafe/jev-1", p3.value)
	}
	// On a variant without an embedded jailbreak gate the phase-3 sentinel
	// broadens to JAILBREAK.
	wantScope := "injection + extraction"
	if !variantHasEmbeddedPhase2() {
		wantScope = "injection + jailbreak + extraction"
	}
	if !strings.Contains(p3.value, wantScope) {
		t.Fatalf("phase3 row = %q, want scope %q", p3.value, wantScope)
	}
}
