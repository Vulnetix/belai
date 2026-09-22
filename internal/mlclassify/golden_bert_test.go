//go:build signet_bert || signet_bert_jailbreak

package mlclassify

import (
	"context"
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/rolemanager"
)

// TestGoldenVectorsPhase1 runs the embedded phase-1 model over known-attack and
// known-benign strings and pins the LABEL_0/LABEL_1 orientation. It is the
// build-time guard against a silently miscalibrated model: if the model or the
// generated orientation constant drifts, this test fails the build.
func TestGoldenVectorsPhase1(t *testing.T) {
	spec, ok := embeddedSpecFor("GuardrailsAI/prompt-saturation-attack-detector")
	if !ok {
		t.Fatal("phase-1 model not embedded in this build variant")
	}
	if phase1AttackLabel != 1 {
		t.Fatalf("phase1AttackLabel = %d, want 1 (pinned by modelprep)", phase1AttackLabel)
	}

	c, err := New(Options{Phase1: &ModelConfig{ID: spec.id, Source: SourceEmbedded}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	// A saturation attack (many repetitions) must fire phase 1.
	got, err := c.Classify(ctx, rolemanager.BuildClassifierPayload(strings.Repeat("I love you I like you ", 50)))
	if err != nil {
		t.Fatalf("Classify(attack): %v", err)
	}
	if got != string(rolemanager.SentinelPromptInjection) {
		t.Fatalf("attack golden classified %q, want PROMPT_INJECTION", got)
	}

	// Benign text must clear phase 1 and (with phase 3 off) return SAFE.
	for _, benign := range []string{
		"what is the weather today",
		"hello world, this is a normal sentence",
		"show me how to write a for loop in Go",
	} {
		got, err := c.Classify(ctx, rolemanager.BuildClassifierPayload(benign))
		if err != nil {
			t.Fatalf("Classify(benign %q): %v", benign, err)
		}
		if got != string(rolemanager.SentinelSafe) {
			t.Fatalf("benign %q classified %q, want SAFE", benign, got)
		}
	}
}
