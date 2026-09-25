package run

import (
	"context"
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/mlclassify"
	"github.com/vulnetix/signet/internal/tools"
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
	// An explicit "llm" is honoured on every build: embedding a model only
	// changes the default. An embedded build used to ignore it silently.
	if got := ClassifierKind(&config.ClassifierSettings{Kind: "llm"}); got != "llm" {
		t.Fatalf("ClassifierKind(llm) = %q, want llm", got)
	}
}

func TestResolveSecurityClassifierDefaultKind(t *testing.T) {
	sc := ResolveSecurityClassifier(nil)
	if mlclassify.Embedded() {
		if sc.Kind != "models" {
			t.Fatalf("Kind = %q, want models on the embedded build", sc.Kind)
		}
		// Phase 1 is embedded and always on; phase 2 is opt-in; phase 3
		// inherits the main model, so tool output is classified by default.
		if sc.Phase1 == nil || sc.Phase2 != nil || !sc.Phase3On {
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

func TestResolveSecurityClassifierModelsPhase3Default(t *testing.T) {
	// Phase 3 is on by default: with provider and model both unset it inherits
	// the main model, because the windowed phase 1 flags nothing on its own.
	inherit := ResolveSecurityClassifier(&config.ClassifierSettings{Kind: "models"})
	if !inherit.Phase3On {
		t.Fatal("phase 3 must inherit the main model when provider/model are unset")
	}
	// A half-set pair is a misconfiguration and stays off.
	modelOnly := ResolveSecurityClassifier(&config.ClassifierSettings{Kind: "models", Model: "gpt-5"})
	if modelOnly.Phase3On {
		t.Fatal("phase 3 must be off when provider is unset")
	}
	on := ResolveSecurityClassifier(&config.ClassifierSettings{Kind: "models", Provider: "openai", Model: "gpt-5"})
	if !on.Phase3On {
		t.Fatal("phase 3 must be on when provider and model are both set")
	}
}

func TestResolveSecurityClassifierPhase3ForeignModelOff(t *testing.T) {
	// A phase-3 model namespaced to a different built-in provider can never be
	// served by the configured provider, so phase 3 must stay off rather than
	// run a stale "openrouter/free" against huggingface.
	sc := ResolveSecurityClassifier(&config.ClassifierSettings{
		Kind:     "models",
		Provider: "huggingface",
		Model:    "openrouter/free",
	})
	if sc.Phase3On {
		t.Fatal("phase 3 must be off when the model is foreign to the provider")
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
	// Even with a HuggingFace token, the untagged build resolves no phase-1
	// model: HF serverless inference cannot serve the known saturation model
	// (its repo ships no tokenizer files), so a token alone must not fabricate
	// a remote default that 400s every prompt.
	t.Setenv("HF_TOKEN", "hf-x")
	t.Setenv("HUGGINGFACE_TOKEN", "")
	sc := ResolveSecurityClassifier(&config.ClassifierSettings{Kind: "models"})
	if mlclassify.Embedded() {
		if sc.Phase1 == nil {
			t.Fatal("phase1 must be embedded and configured on the embedded build")
		}
		return
	}
	// On the untagged build with no embedded model and no explicit phase-1
	// config, phase 1 is absent regardless of token presence — the ML stack
	// cannot run, which the pipeline fails closed on rather than silently
	// downgrading to the LLM path.
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

// TestModelsKindNoPhaseModelFailsClosedWithActionableError pins that a
// models-kind config with no resolvable phase model (a no-classifier binary
// with no explicit phase) fails closed with the actionable error naming the
// ways out, not the opaque "no phase configured" repeated per prompt.
func TestModelsKindNoPhaseModelFailsClosedWithActionableError(t *testing.T) {
	if mlclassify.Embedded() {
		t.Skip("embedded build always resolves a phase model")
	}
	cfg := Config{Provider: "openai", BaseURL: "https://example.invalid/v1", APIKey: "k", Model: "gpt-5"}
	cfg.Security = ResolveSecurityClassifier(&config.ClassifierSettings{Kind: "models"})
	if cfg.Security.Phase1 != nil || cfg.Security.Phase2 != nil {
		t.Fatalf("setup: security config resolved phases: %+v", cfg.Security)
	}
	p := NewPipelineWithRetry(cfg, nil, nil, nil)
	_, err := p.Process(context.Background(), tools.Result{Kind: tools.KindBash, Content: "echo hi"})
	if err == nil {
		t.Fatal("Process must fail closed when the models path has no phase model")
	}
	for _, want := range []string{"no phase model", "just build-bert", "\"llm\""} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q must contain %q", err.Error(), want)
		}
	}
}

func TestPreloadClassifierNoopWhenNoPhaseConfigured(t *testing.T) {
	// A no-classifier binary with kind "models" but no resolvable phase model
	// must not hard-fail startup: there is no embedded model to load, and the
	// pipeline fails closed at use time instead. This is the no-phase case, not
	// the embedded-model-load-failure case PreloadClassifier exists to catch.
	if err := PreloadClassifier(SecurityClassifierConfig{Kind: "models"}); err != nil {
		t.Fatalf("PreloadClassifier(models, no phases) = %v, want nil", err)
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

func TestResolveClassifierModelsPathResolvesPhase3Guard(t *testing.T) {
	// On the models path classifier.provider/model resolve the guardrail
	// classifier (phase 3), not the role classifier. A Jev classifier on
	// openrouter must resolve to openrouter/typesafe/jev, never the main model.
	main := Config{Provider: "cloudflare-ai-gateway", BaseURL: "https://gw.example", APIKey: "k", Model: "@cf/deepseek-ai/deepseek-v4-pro-0813"}
	cls := &config.ClassifierSettings{Kind: "models", Provider: "openrouter", Model: "typesafe/jev-1.13"}
	cc, err := ResolveClassifier(main, cls, fakeSource{vals: map[string]string{"openrouter:api_key": "or-key"}})
	if err != nil {
		t.Fatalf("ResolveClassifier: %v", err)
	}
	if cc.Provider != "openrouter" || cc.Model != "typesafe/jev-1.13" || cc.APIKey != "or-key" {
		t.Fatalf("models-path guard must resolve classifier.provider/model: %+v", cc)
	}
}
