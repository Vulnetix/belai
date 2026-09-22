package run

import (
	"testing"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/mlclassify"
)

func TestClassifierKindDefault(t *testing.T) {
	want := "llm"
	if mlclassify.Embedded() {
		want = "models"
	}
	if got := ClassifierKind(nil); got != want {
		t.Fatalf("ClassifierKind(nil) = %q, want %q", got, want)
	}
	if got := ClassifierKind(&config.ClassifierSettings{}); got != want {
		t.Fatalf("ClassifierKind(empty) = %q, want %q", got, want)
	}
	if got := ClassifierKind(&config.ClassifierSettings{Kind: "models"}); got != "models" {
		t.Fatalf("ClassifierKind(models) = %q, want models", got)
	}
}

func TestResolveSecurityClassifierDefaultKind(t *testing.T) {
	sc := ResolveSecurityClassifier(nil)
	if mlclassify.Embedded() {
		if sc.Kind != "models" {
			t.Fatalf("Kind = %q, want models on the embedded build", sc.Kind)
		}
		// Phase 1 is embedded and always on; phase 2 is opt-in; phase 3 off.
		if sc.Phase1 == nil || sc.Phase2 != nil || sc.Phase3On {
			t.Fatalf("embedded default security config = %+v", sc)
		}
		return
	}
	if sc.Kind != "llm" {
		t.Fatalf("Kind = %q, want llm", sc.Kind)
	}
	if sc.Phase1 != nil || sc.Phase2 != nil || sc.Phase3On {
		t.Fatalf("llm security config must carry no phases: %+v", sc)
	}
}

func TestResolveSecurityClassifierModelsPhase3OptIn(t *testing.T) {
	// Phase 3 is opt-in: only an explicit provider AND model enable it.
	off := ResolveSecurityClassifier(&config.ClassifierSettings{Kind: "models"})
	if off.Phase3On {
		t.Fatal("phase 3 must be off when provider/model are unset")
	}
	modelOnly := ResolveSecurityClassifier(&config.ClassifierSettings{Kind: "models", Model: "gpt-5"})
	if modelOnly.Phase3On {
		t.Fatal("phase 3 must be off when provider is unset")
	}
	on := ResolveSecurityClassifier(&config.ClassifierSettings{Kind: "models", Provider: "openai", Model: "gpt-5"})
	if !on.Phase3On {
		t.Fatal("phase 3 must be on when provider and model are both set")
	}
}

func TestResolveSecurityClassifierPhaseConfigs(t *testing.T) {
	cls := &config.ClassifierSettings{
		Kind:   "models",
		Phase1: config.ClassifierPhaseSettings{Model: "GuardrailsAI/prompt-saturation-attack-detector", Source: "huggingface", Threshold: 0.7},
		Phase2: config.ClassifierPhaseSettings{Source: "disabled"},
	}
	sc := ResolveSecurityClassifier(cls)
	if sc.Phase1 == nil {
		t.Fatal("phase1 must be configured")
	}
	if sc.Phase1.ID != "GuardrailsAI/prompt-saturation-attack-detector" ||
		sc.Phase1.Source != mlclassify.SourceHuggingFace ||
		sc.Phase1.Threshold != 0.7 ||
		sc.Phase1.AttackLabel != "LABEL_1" {
		t.Fatalf("phase1 = %+v", sc.Phase1)
	}
	if sc.Phase2 != nil {
		t.Fatalf("phase2 must be nil when source is disabled, got %+v", sc.Phase2)
	}
}

func TestResolveSecurityClassifierPhase2RemoteLabel(t *testing.T) {
	cls := &config.ClassifierSettings{
		Kind:   "models",
		Phase2: config.ClassifierPhaseSettings{Model: "leomaurodesenv/bert-base-uncased-trustairlab-jailbreak", Source: "huggingface"},
	}
	sc := ResolveSecurityClassifier(cls)
	if sc.Phase2 == nil {
		t.Fatal("phase2 must be configured")
	}
	if sc.Phase2.AttackLabel != "unsafe" {
		t.Fatalf("phase2 AttackLabel = %q, want unsafe", sc.Phase2.AttackLabel)
	}
}

func TestResolveSecurityClassifierModelsPhase1(t *testing.T) {
	sc := ResolveSecurityClassifier(&config.ClassifierSettings{Kind: "models"})
	if mlclassify.Embedded() {
		if sc.Phase1 == nil {
			t.Fatal("phase1 must be embedded and configured on the embedded build")
		}
		return
	}
	// On the untagged build with no embedded model and no explicit phase-1
	// config, phase 1 is absent — the ML stack cannot run, which the pipeline
	// treats as a build failure rather than a silent LLM downgrade.
	if sc.Phase1 != nil {
		t.Fatalf("phase1 = %+v, want nil without embedded model or explicit config", sc.Phase1)
	}
}

func TestPreloadClassifierNoopForLLM(t *testing.T) {
	if err := PreloadClassifier(SecurityClassifierConfig{Kind: "llm"}); err != nil {
		t.Fatalf("PreloadClassifier(llm) = %v, want nil", err)
	}
	if err := PreloadClassifier(SecurityClassifierConfig{}); err != nil {
		t.Fatalf("PreloadClassifier(empty) = %v, want nil", err)
	}
}

func TestResolveClassifierKindLLMStillInherits(t *testing.T) {
	// Regression: the LLM sentinel path keeps inheritance. A models-kind
	// setting must not leak the no-inheritance rule into the LLM path.
	main := Config{Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "k", Model: "gpt-5"}
	cc, err := ResolveClassifier(main, &config.ClassifierSettings{Kind: "models"}, nil)
	if err != nil {
		t.Fatalf("ResolveClassifier: %v", err)
	}
	if cc.Provider != "openai" || cc.Model != "gpt-5" {
		t.Fatalf("LLM classifier must still inherit main model: %+v", cc)
	}
}
