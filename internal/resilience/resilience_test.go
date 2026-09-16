package resilience

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestDelayHonoursRetryAfter(t *testing.T) {
	p := Policy{Ceiling: 5 * time.Second}
	got := p.Delay(0, 2*time.Second, 0)
	if got != 2*time.Second {
		t.Fatalf("Delay = %v, want 2s", got)
	}
}

func TestDelayCapsRetryAfter(t *testing.T) {
	p := Policy{Ceiling: 5 * time.Second}
	got := p.Delay(0, 10*time.Minute, 0)
	if got != 5*time.Second {
		t.Fatalf("Delay = %v, want 5s cap", got)
	}
}

func TestDelayExponential(t *testing.T) {
	p := Policy{Base: 100 * time.Millisecond, Cap: 10 * time.Second, Jitter: 0}
	for i, want := range []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond} {
		got := p.Delay(i, 0, 0)
		if got != want {
			t.Fatalf("Delay(%d) = %v, want %v", i, got, want)
		}
	}
}

func TestDelayCapAt8Seconds(t *testing.T) {
	p := Policy{Base: 500 * time.Millisecond, Cap: 8 * time.Second, Jitter: 0}
	got := p.Delay(10, 0, 0)
	if got != 8*time.Second {
		t.Fatalf("Delay = %v, want 8s", got)
	}
}

func TestDelayJitterDownward(t *testing.T) {
	p := Policy{Base: 100 * time.Millisecond, Jitter: 0.5}
	got := p.Delay(0, 0, 1)
	if got != 50*time.Millisecond {
		t.Fatalf("Delay = %v, want 50ms", got)
	}
}

func TestParseRetryAfterSeconds(t *testing.T) {
	if got := ParseRetryAfter("3", time.Now()); got != 3*time.Second {
		t.Fatalf("ParseRetryAfter(\"3\") = %v", got)
	}
}

func TestParseRetryAfterHTTPDate(t *testing.T) {
	now := time.Now()
	then := now.Add(90 * time.Second)
	got := ParseRetryAfter(then.UTC().Format(http.TimeFormat), now)
	if got < 89*time.Second || got > 91*time.Second {
		t.Fatalf("ParseRetryAfter date = %v", got)
	}
}

func TestParseRetryAfterGarbageReturnsZero(t *testing.T) {
	if got := ParseRetryAfter("never", time.Now()); got != 0 {
		t.Fatalf("ParseRetryAfter garbage = %v, want 0", got)
	}
}

func TestDoSucceedsFirstTry(t *testing.T) {
	p := Policy{}
	c := FuncClassifier(func(error) Verdict { return Verdict{Class: ClassRetryable} })
	calls := 0
	v, err := Do(context.Background(), p, c, func(context.Context) (int, error) {
		calls++
		return 42, nil
	}, nil)
	if err != nil || v != 42 || calls != 1 {
		t.Fatalf("got %d/%d err=%v", v, calls, err)
	}
}

func TestDoRetriesThenSucceeds(t *testing.T) {
	p := Policy{MaxAttempts: 3, Base: time.Millisecond, Cap: time.Millisecond, Jitter: 0}
	c := FuncClassifier(func(error) Verdict { return Verdict{Class: ClassRetryable} })
	calls := 0
	v, err := Do(context.Background(), p, c, func(context.Context) (int, error) {
		calls++
		if calls < 3 {
			return 0, errors.New("boom")
		}
		return 7, nil
	}, nil)
	if err != nil || v != 7 || calls != 3 {
		t.Fatalf("got %d/%d err=%v", v, calls, err)
	}
}

func TestDoStopsAtMaxAttempts(t *testing.T) {
	p := Policy{MaxAttempts: 3, Base: time.Millisecond, Cap: time.Millisecond, Jitter: 0}
	c := FuncClassifier(func(error) Verdict { return Verdict{Class: ClassRetryable} })
	calls := 0
	_, err := Do(context.Background(), p, c, func(context.Context) (int, error) {
		calls++
		return 0, errors.New("boom")
	}, nil)
	if err == nil || calls != 3 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestDoFatalDoesNotRetry(t *testing.T) {
	p := Policy{MaxAttempts: 3, Base: time.Millisecond}
	c := FuncClassifier(func(error) Verdict { return Verdict{Class: ClassFatal} })
	calls := 0
	_, err := Do(context.Background(), p, c, func(context.Context) (int, error) {
		calls++
		return 0, errors.New("denied")
	}, nil)
	if err == nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestDoCallsOnRetry(t *testing.T) {
	p := Policy{MaxAttempts: 3, Base: time.Millisecond, Cap: time.Millisecond, Jitter: 0}
	c := FuncClassifier(func(error) Verdict { return Verdict{Class: ClassRetryable} })
	var attempts []Attempt
	_, _ = Do(context.Background(), p, c, func(context.Context) (int, error) {
		return 0, errors.New("boom")
	}, func(a Attempt) { attempts = append(attempts, a) })
	if len(attempts) != 2 {
		t.Fatalf("onRetry calls = %d, want 2", len(attempts))
	}
	if attempts[0].Attempt != 2 || attempts[1].Attempt != 3 {
		t.Fatalf("attempt numbers = %v", attempts)
	}
}

func TestDoCancelDuringBackoff(t *testing.T) {
	p := Policy{MaxAttempts: 3, Base: 10 * time.Second, Jitter: 0}
	c := FuncClassifier(func(error) Verdict { return Verdict{Class: ClassRetryable} })
	ctx, cancel := context.WithCancel(context.Background())
	p.Sleep = func(c context.Context, d time.Duration) error {
		cancel()
		return c.Err()
	}
	_, err := Do(ctx, p, c, func(context.Context) (int, error) {
		return 0, errors.New("boom")
	}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestDefaultClassifierRetryableStatus(t *testing.T) {
	c := DefaultClassifier{}
	v := c.Classify(&statusErr{code: 503})
	if v.Class != ClassRetryable {
		t.Fatalf("503 -> %v, want retryable", v.Class)
	}
}

func TestDefaultClassifierFatalStatus(t *testing.T) {
	c := DefaultClassifier{}
	v := c.Classify(&statusErr{code: 401})
	if v.Class != ClassFatal {
		t.Fatalf("401 -> %v, want fatal", v.Class)
	}
}

func TestDefaultClassifierDenyList(t *testing.T) {
	c := DefaultClassifier{}
	v := c.Classify(errors.New("insufficient_quota: please check billing"))
	if v.Class != ClassFatal {
		t.Fatalf("denylist -> %v, want fatal", v.Class)
	}
}

func TestDefaultClassifierOverflow(t *testing.T) {
	c := DefaultClassifier{}
	v := c.Classify(errors.New("context length exceeded"))
	if v.Class != ClassOverflow {
		t.Fatalf("overflow -> %v, want overflow", v.Class)
	}
}

func TestDefaultClassifierAllowList(t *testing.T) {
	c := DefaultClassifier{}
	v := c.Classify(errors.New("stream ended without finish_reason"))
	if v.Class != ClassRetryable {
		t.Fatalf("allow -> %v, want retryable", v.Class)
	}
}

type statusErr struct {
	code int
}

func (e *statusErr) Error() string   { return "status" }
func (e *statusErr) StatusCode() int { return e.code }

func TestWithDefaultsFillsEveryZeroField(t *testing.T) {
	p := Policy{}.WithDefaults()

	if p.MaxAttempts != 3 {
		t.Fatalf("MaxAttempts = %d, want 3", p.MaxAttempts)
	}
	if p.Base != 500*time.Millisecond {
		t.Fatalf("Base = %v, want 500ms", p.Base)
	}
	if p.Cap != 8*time.Second {
		t.Fatalf("Cap = %v, want 8s", p.Cap)
	}
	if p.Ceiling != 60*time.Second {
		t.Fatalf("Ceiling = %v, want 60s", p.Ceiling)
	}
	// Jitter is the one field with no non-zero default: zero means "no
	// jitter", and each call site opts in (Signet's policies use 0.25).
	if p.Jitter != 0 {
		t.Fatalf("Jitter = %v, want 0", p.Jitter)
	}
	// Callers outside the package read these directly; a nil func panics.
	if p.Rand == nil {
		t.Fatal("Rand is nil after WithDefaults")
	}
	if p.Sleep == nil {
		t.Fatal("Sleep is nil after WithDefaults")
	}
	if r := p.Rand(); r < 0 || r >= 1 {
		t.Fatalf("Rand() = %v, want [0,1)", r)
	}
}

func TestWithDefaultsKeepsExplicitValues(t *testing.T) {
	sentinel := func() float64 { return 0.5 }
	p := Policy{
		MaxAttempts: 7,
		Base:        time.Millisecond,
		Cap:         2 * time.Millisecond,
		Ceiling:     3 * time.Millisecond,
		Jitter:      0.75,
		Rand:        sentinel,
	}.WithDefaults()

	if p.MaxAttempts != 7 || p.Base != time.Millisecond || p.Cap != 2*time.Millisecond ||
		p.Ceiling != 3*time.Millisecond || p.Jitter != 0.75 {
		t.Fatalf("WithDefaults overwrote explicit values: %+v", p)
	}
	if p.Rand() != 0.5 {
		t.Fatal("WithDefaults replaced an explicit Rand")
	}
}

func TestWithDefaultsClampsJitter(t *testing.T) {
	if got := (Policy{Jitter: -1}).WithDefaults().Jitter; got != 0 {
		t.Fatalf("Jitter = %v, want 0", got)
	}
	if got := (Policy{Jitter: 5}).WithDefaults().Jitter; got != 1 {
		t.Fatalf("Jitter = %v, want 1", got)
	}
}

func TestWithDefaultsSleepHonoursContext(t *testing.T) {
	p := Policy{}.WithDefaults()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Sleep(ctx, time.Hour); err == nil {
		t.Fatal("expected cancelled context to end the sleep")
	}
}
