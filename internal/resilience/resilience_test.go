package resilience

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"syscall"
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
	if attempts[0].Max != 3 || attempts[1].Max != 3 {
		t.Fatalf("attempt max = %v, want 3", attempts)
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

func TestDefaultClassifierRateLimit429UsesDefaultRetryAfter(t *testing.T) {
	c := DefaultClassifier{}
	v := c.Classify(&statusErr{code: http.StatusTooManyRequests})
	if v.Class != ClassRetryable {
		t.Fatalf("429 -> %v, want retryable", v.Class)
	}
	if v.RetryAfter != DefaultRateLimitRetryAfter {
		t.Fatalf("RetryAfter = %v, want %v", v.RetryAfter, DefaultRateLimitRetryAfter)
	}
}

func TestDefaultClassifierRateLimitTextUsesDefaultRetryAfter(t *testing.T) {
	c := DefaultClassifier{}
	v := c.Classify(errors.New("inference request per min rate reached"))
	if v.Class != ClassRetryable {
		t.Fatalf("rate-limit text -> %v, want retryable", v.Class)
	}
	if v.RetryAfter != DefaultRateLimitRetryAfter {
		t.Fatalf("RetryAfter = %v, want %v", v.RetryAfter, DefaultRateLimitRetryAfter)
	}
}

func TestDefaultClassifierExplicitRetryAfterOverridesDefault(t *testing.T) {
	c := DefaultClassifier{}
	v := c.Classify(&retryAfterErr{code: http.StatusTooManyRequests, retryAfter: 5 * time.Second})
	if v.Class != ClassRetryable {
		t.Fatalf("explicit retry-after -> %v, want retryable", v.Class)
	}
	if v.RetryAfter != 5*time.Second {
		t.Fatalf("RetryAfter = %v, want 5s", v.RetryAfter)
	}
}

func TestDefaultClassifierDenyListQuotaRemainsFatal(t *testing.T) {
	c := DefaultClassifier{}
	v := c.Classify(errors.New("insufficient_quota: please check billing"))
	if v.Class != ClassFatal {
		t.Fatalf("denylist quota -> %v, want fatal", v.Class)
	}
}

// timeoutErr is a net.Error that reports a timeout without being a context
// error, as a dial or read deadline does.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "read tcp: i/o deadline reached" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func postErr(u string, err error) error {
	return fmt.Errorf("request: %w", &url.Error{Op: "Post", URL: u, Err: err})
}

func TestDefaultClassifierTransportErrors(t *testing.T) {
	const gw = "https://gateway.ai.cloudflare.com/v1/acct/default/compat/v1/chat/completions"
	cases := []struct {
		name string
		err  error
		want Class
	}{
		{"h2 protocol error from peer", postErr(gw, errors.New("stream error: stream ID 85; PROTOCOL_ERROR; received from peer")), ClassRetryable},
		{"h2 goaway", postErr(gw, errors.New("http2: server sent GOAWAY and closed the connection; LastStreamID=85, ErrCode=NO_ERROR, debug=\"\"")), ClassRetryable},
		{"h2 refused stream", postErr(gw, errors.New("stream error: stream ID 3; REFUSED_STREAM")), ClassRetryable},
		{"mid-stream internal error", errors.New("stream read: stream error: stream ID 9; INTERNAL_ERROR; received from peer"), ClassRetryable},
		{"client connection lost", postErr(gw, errors.New("http2: client connection lost")), ClassRetryable},
		{"stream idle timeout", errors.New("stream idle timeout after 2m0s"), ClassRetryable},
		{"econnreset", postErr(gw, &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", syscall.ECONNRESET)}), ClassRetryable},
		{"unexpected eof", fmt.Errorf("stream read: %w", io.ErrUnexpectedEOF), ClassRetryable},
		{"pooled conn eof", postErr(gw, io.EOF), ClassRetryable},
		{"net timeout", postErr(gw, timeoutErr{}), ClassRetryable},
		{"url words are not scanned", postErr("https://payment.example/quota/token_limit", errors.New("connection reset by peer")), ClassRetryable},
		{"wrapped 503", fmt.Errorf("turn: %w", &statusErr{code: 503}), ClassRetryable},
		{"wrapped 401", fmt.Errorf("turn: %w", &statusErr{code: 401}), ClassFatal},
		{"x509 unknown authority", postErr(gw, errors.New("tls: failed to verify certificate: x509: certificate signed by unknown authority")), ClassFatal},
		{"bare eof is not a transport fault", io.EOF, ClassFatal},
		{"unrecognised", errors.New("something odd"), ClassFatal},
		{"cancelled", postErr(gw, context.Canceled), ClassAborted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := (DefaultClassifier{}).Classify(tc.err).Class; got != tc.want {
				t.Fatalf("Classify(%q) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestDefaultClassifierEnhanceYourCalmIsRateLimited(t *testing.T) {
	v := DefaultClassifier{}.Classify(errors.New("stream error: stream ID 5; ENHANCE_YOUR_CALM"))
	if v.Class != ClassRetryable || v.RetryAfter != DefaultRateLimitRetryAfter {
		t.Fatalf("ENHANCE_YOUR_CALM -> %+v, want retryable with %v", v, DefaultRateLimitRetryAfter)
	}
}

func TestDefaultClassifierWrappedRetryAfter(t *testing.T) {
	v := DefaultClassifier{}.Classify(fmt.Errorf("turn: %w", &retryAfterErr{code: 503, retryAfter: 7 * time.Second}))
	if v.Class != ClassRetryable || v.RetryAfter != 7*time.Second {
		t.Fatalf("wrapped retry-after -> %+v", v)
	}
}

type retryAfterErr struct {
	code       int
	retryAfter time.Duration
}

func (e *retryAfterErr) Error() string             { return "rate limited" }
func (e *retryAfterErr) StatusCode() int           { return e.code }
func (e *retryAfterErr) RetryAfter() time.Duration { return e.retryAfter }

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
