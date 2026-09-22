package rolemanager

import (
	"context"
	"errors"
	"testing"

	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/tools"
)

// TestSecurityClassifierTakesPrecedence proves the ML stack (Security) is the
// classifier the security path uses, while Classifier remains available for
// the sentinel families (mode/goal/plan) that a binary classifier cannot emit.
func TestSecurityClassifierTakesPrecedence(t *testing.T) {
	pipe := NewPipeline(stubClassifier{reply: "SAFE"})
	pipe.Security = stubClassifier{reply: "JAILBREAK"}

	dec, err := pipe.Process(context.Background(), tools.ReadResult("content"))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if dec.Sentinel != SentinelJailbreak {
		t.Fatalf("Sentinel = %q, want JAILBREAK from the Security classifier", dec.Sentinel)
	}
	if dec.Action != ActionWarn {
		t.Fatalf("Action = %q, want warn", dec.Action)
	}
}

// TestClassifierAloneStillServes proves the Classifier field is unchanged when
// Security is nil: it remains the single classifier for every path.
func TestClassifierAloneStillServes(t *testing.T) {
	pipe := NewPipeline(stubClassifier{reply: "SAFE"})
	dec, err := pipe.Process(context.Background(), tools.ReadResult("content"))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if dec.Sentinel != SentinelSafe {
		t.Fatalf("Sentinel = %q, want SAFE", dec.Sentinel)
	}
}

// TestAdmitIgnoreBothSkipsSecurityClassifier mirrors the existing
// posture-ignore test but on the Security field: guardrails-off must mean zero
// inference, so a Security classifier that errors if called must never be
// called under an ignore-all posture.
func TestAdmitIgnoreBothSkipsSecurityClassifier(t *testing.T) {
	pipe := NewPipeline(stubClassifier{err: errors.New("should not be called")})
	pipe.Security = stubClassifier{err: errors.New("security should not be called")}
	pol := posture.Policy{posture.PromptUnsafe: posture.Ignore, posture.PromptMalformed: posture.Ignore}
	dec, err := pipe.Admit(context.Background(), "hello", "test", pol)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.Action != ActionProceed {
		t.Fatalf("expected proceed, got %s", dec.Action)
	}
}

// TestSecurityMalformedFailsClosed proves the empty-string contract: an ML
// phase-3 reply that fails the narrowed parse reaches the pipeline as an empty
// string, which ParseSentinel rejects as malformed.
func TestSecurityMalformedFailsClosed(t *testing.T) {
	pipe := NewPipeline(stubClassifier{reply: "SAFE"})
	pipe.Security = stubClassifier{reply: ""} // malformed phase-3 reply
	dec, err := pipe.Process(context.Background(), tools.ReadResult("content"))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if dec.Action != ActionWarn {
		t.Fatalf("Action = %q, want warn for malformed Security reply", dec.Action)
	}
}

// TestClassifierIdentityKeysCache proves verdicts from different classifier
// identities never share a cache bucket.
func TestClassifierIdentityKeysCache(t *testing.T) {
	cache := NewCache(16, "")
	a := NewPipeline(stubClassifier{reply: "SAFE"})
	a.Cache = cache
	b := NewPipeline(stubClassifier{reply: "PROMPT_INJECTION"})
	b.Cache = cache
	b.SetClassifierIdentity("models;phase1=x")

	// The first pipeline stores SAFE under the content-only key.
	if _, err := a.Process(context.Background(), tools.ReadResult("same content")); err != nil {
		t.Fatalf("Process a: %v", err)
	}
	// The second pipeline has a different identity, so the same content must
	// NOT hit the first pipeline's SAFE verdict; it classifies fresh and
	// stores PROMPT_INJECTION under its own key.
	dec, err := b.Process(context.Background(), tools.ReadResult("same content"))
	if err != nil {
		t.Fatalf("Process b: %v", err)
	}
	if dec.Sentinel != SentinelPromptInjection {
		t.Fatalf("Sentinel = %q, want PROMPT_INJECTION (identities must not collide)", dec.Sentinel)
	}
}

// TestKeyForDistinctIdentities proves the cache key function itself separates
// identities.
func TestKeyForDistinctIdentities(t *testing.T) {
	if KeyFor("a", "x") == KeyFor("b", "x") {
		t.Fatal("KeyFor must separate classifier identities")
	}
	if KeyFor("", "x") != Key("x") {
		t.Fatal("KeyFor with empty identity must equal content-only Key")
	}
}
