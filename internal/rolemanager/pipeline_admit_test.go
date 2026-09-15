package rolemanager

import (
	"errors"
	"testing"

	"github.com/vulnetix/signet/internal/posture"
)

type stubClassifier struct {
	reply string
	err   error
}

func (s stubClassifier) Classify(p ClassifierPayload) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return s.reply, nil
}

func TestAdmitSafeProceeds(t *testing.T) {
	pipe := NewPipeline(stubClassifier{reply: "SAFE"})
	dec, err := pipe.Admit("hello", posture.Defaults())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.Action != ActionProceed {
		t.Fatalf("expected proceed, got %s", dec.Action)
	}
	if dec.Sentinel != SentinelSafe {
		t.Fatalf("expected SAFE sentinel")
	}
}

func TestAdmitUnsafeEnforceRefuses(t *testing.T) {
	pipe := NewPipeline(stubClassifier{reply: "PROMPT_INJECTION"})
	_, err := pipe.Admit("bad", posture.Defaults())
	if err == nil {
		t.Fatal("expected refusal error")
	}
	var re *RefusalError
	if !errors.As(err, &re) {
		t.Fatalf("expected *RefusalError, got %T", err)
	}
	if re.Sentinel != SentinelPromptInjection {
		t.Fatalf("expected PROMPT_INJECTION, got %s", re.Sentinel)
	}
	if !errors.Is(err, err) { // basic check
		_ = err.Error()
	}
	if err.Error() == "" {
		t.Fatal("error message should not be empty")
	}
}

func TestAdmitUnsafeWarnProceeds(t *testing.T) {
	pipe := NewPipeline(stubClassifier{reply: "PROMPT_INJECTION"})
	pol := posture.Policy{posture.PromptUnsafe: posture.Warn}
	dec, err := pipe.Admit("bad", pol)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.Action != ActionWarn {
		t.Fatalf("expected warn, got %s", dec.Action)
	}
}

func TestAdmitUnsafeIgnoreProceeds(t *testing.T) {
	pipe := NewPipeline(stubClassifier{reply: "PROMPT_INJECTION"})
	pol := posture.Policy{posture.PromptUnsafe: posture.Ignore}
	dec, err := pipe.Admit("bad", pol)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.Action != ActionProceed {
		t.Fatalf("expected proceed under ignore, got %s", dec.Action)
	}
}

func TestAdmitMalformedEnforceRefuses(t *testing.T) {
	pipe := NewPipeline(stubClassifier{reply: "not-a-sentinel"})
	_, err := pipe.Admit("bad", posture.Defaults())
	if err == nil {
		t.Fatal("expected refusal error")
	}
	var re *RefusalError
	if !errors.As(err, &re) {
		t.Fatalf("expected *RefusalError, got %T", err)
	}
	if re.Sentinel != SentinelMalformed {
		t.Fatalf("expected MALFORMED sentinel, got %s", re.Sentinel)
	}
}

func TestAdmitMalformedWarnProceeds(t *testing.T) {
	pipe := NewPipeline(stubClassifier{reply: "nope"})
	pol := posture.Policy{posture.PromptMalformed: posture.Warn}
	dec, err := pipe.Admit("bad", pol)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.Action != ActionWarn {
		t.Fatalf("expected warn, got %s", dec.Action)
	}
}

func TestAdmitMalformedIgnoreProceeds(t *testing.T) {
	pipe := NewPipeline(stubClassifier{reply: "nope"})
	pol := posture.Policy{posture.PromptMalformed: posture.Ignore}
	dec, err := pipe.Admit("bad", pol)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.Action != ActionProceed {
		t.Fatalf("expected proceed under ignore, got %s", dec.Action)
	}
}

func TestAdmitIgnoreBothSkipsClassifier(t *testing.T) {
	pipe := NewPipeline(stubClassifier{err: errors.New("should not be called")})
	pol := posture.Policy{posture.PromptUnsafe: posture.Ignore, posture.PromptMalformed: posture.Ignore}
	dec, err := pipe.Admit("hello", pol)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.Action != ActionProceed {
		t.Fatalf("expected proceed, got %s", dec.Action)
	}
}

func TestAdmitPropagatesClassifierError(t *testing.T) {
	pipe := NewPipeline(stubClassifier{err: errors.New("boom")})
	_, err := pipe.Admit("hello", posture.Defaults())
	if err == nil {
		t.Fatal("expected error")
	}
}
