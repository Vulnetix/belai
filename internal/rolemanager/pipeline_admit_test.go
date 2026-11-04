package rolemanager

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/posture"
)

type stubClassifier struct {
	reply string
	err   error
}

func (s stubClassifier) Classify(ctx context.Context, p ClassifierPayload) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return s.reply, nil
}

func TestAdmitSafeProceeds(t *testing.T) {
	pipe := NewPipeline(stubClassifier{reply: "SAFE"})
	dec, err := pipe.Admit(context.Background(), "hello", "test", posture.Defaults())
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
	_, err := pipe.Admit(context.Background(), "bad", "test", posture.Defaults())
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
	dec, err := pipe.Admit(context.Background(), "bad", "test", pol)
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
	dec, err := pipe.Admit(context.Background(), "bad", "test", pol)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.Action != ActionProceed {
		t.Fatalf("expected proceed under ignore, got %s", dec.Action)
	}
}

func TestAdmitMalformedEnforceRefuses(t *testing.T) {
	pipe := NewPipeline(stubClassifier{reply: "not-a-sentinel"})
	pipe.RetryWait = func() time.Duration { return 0 }
	_, err := pipe.Admit(context.Background(), "bad", "test", posture.Defaults())
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
	dec, err := pipe.Admit(context.Background(), "bad", "test", pol)
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
	dec, err := pipe.Admit(context.Background(), "bad", "test", pol)
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
	dec, err := pipe.Admit(context.Background(), "hello", "test", pol)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.Action != ActionProceed {
		t.Fatalf("expected proceed, got %s", dec.Action)
	}
}

func TestAdmitPropagatesClassifierError(t *testing.T) {
	pipe := NewPipeline(stubClassifier{err: errors.New("boom")})
	_, err := pipe.Admit(context.Background(), "hello", "test", posture.Defaults())
	if err == nil {
		t.Fatal("expected error")
	}
}

// replyQueue answers each call with the next reply, repeating the last.
type replyQueue struct {
	replies []string
	calls   atomic.Int32
}

func (q *replyQueue) Classify(context.Context, ClassifierPayload) (string, error) {
	n := int(q.calls.Add(1)) - 1
	if n >= len(q.replies) {
		n = len(q.replies) - 1
	}
	return q.replies[n], nil
}

func TestAdmitMalformedRetriesOnceBeforeRefusing(t *testing.T) {
	q := &replyQueue{replies: []string{"", "SAFE"}}
	pipe := NewPipeline(q)
	waits := 0
	pipe.RetryWait = func() time.Duration { waits++; return 0 }
	dec, err := pipe.Admit(context.Background(), "hello", "test", posture.Defaults())
	if err != nil {
		t.Fatalf("a malformed reply followed by SAFE must admit: %v", err)
	}
	if dec.Action != ActionProceed || dec.Sentinel != SentinelSafe {
		t.Fatalf("decision %+v, want proceed SAFE", dec)
	}
	if q.calls.Load() != 2 || waits != 1 {
		t.Fatalf("calls %d waits %d, want 2 and 1", q.calls.Load(), waits)
	}
}

func TestAdmitMalformedTwiceRefusesAfterOneRetry(t *testing.T) {
	q := &replyQueue{replies: []string{"", "garbage"}}
	pipe := NewPipeline(q)
	pipe.RetryWait = func() time.Duration { return 0 }
	_, err := pipe.Admit(context.Background(), "hello", "test", posture.Defaults())
	var re *RefusalError
	if !errors.As(err, &re) || re.Sentinel != SentinelMalformed {
		t.Fatalf("err %v, want a MALFORMED refusal", err)
	}
	if q.calls.Load() != 2 {
		t.Fatalf("calls %d, want exactly one retry", q.calls.Load())
	}
}

func TestAdmitMalformedWarnDoesNotRetry(t *testing.T) {
	q := &replyQueue{replies: []string{"", "SAFE"}}
	pipe := NewPipeline(q)
	pipe.RetryWait = func() time.Duration { t.Fatal("warn must not wait"); return 0 }
	if _, err := pipe.Admit(context.Background(), "hello", "test", posture.Policy{posture.PromptMalformed: posture.Warn}); err != nil {
		t.Fatal(err)
	}
	if q.calls.Load() != 1 {
		t.Fatalf("calls %d, want 1", q.calls.Load())
	}
}

func TestAdmitMalformedRetryStopsOnCancel(t *testing.T) {
	q := &replyQueue{replies: []string{""}}
	pipe := NewPipeline(q)
	pipe.RetryWait = func() time.Duration { return time.Hour }
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	if _, err := pipe.Admit(ctx, "hello", "test", posture.Defaults()); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v, want context.Canceled", err)
	}
}

func TestMalformedRetryWaitIsBounded(t *testing.T) {
	for range 200 {
		d := MalformedRetryWait()
		if d < 2*time.Second || d >= 5*time.Second {
			t.Fatalf("wait %s outside [2s, 5s)", d)
		}
	}
}
