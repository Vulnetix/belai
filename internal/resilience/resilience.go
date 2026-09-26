// Package resilience implements the retry and classification primitives used by
// Belai's model provider path. It is intentionally leaf-only: it imports no
// other belai packages so every caller, including internal/run, can depend on
// it without cycles.
package resilience

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"syscall"
	"time"
)

// Class groups errors into retry buckets. The zero value is ClassFatal, which
// keeps the fail-closed default: an unrecognised error is never retried.
type Class int

const (
	ClassFatal Class = iota
	ClassRetryable
	ClassOverflow
	ClassAborted
)

// Verdict is the classification result for a single error.
type Verdict struct {
	Class      Class
	RetryAfter time.Duration
	Reason     string
}

// Attempt carries the metadata surfaced to observers before each backoff.
type Attempt struct {
	Attempt int
	// Max is the inclusive attempt budget, so observers can render n/max.
	Max    int
	Delay  time.Duration
	Reason string
}

// StatusCoder is implemented by errors that carry an HTTP status code.
type StatusCoder interface{ StatusCode() int }

// RetryAfterer is implemented by errors that carry an explicit Retry-After hint.
type RetryAfterer interface{ RetryAfter() time.Duration }

// Policy controls the exponential backoff. Zero values are replaced with
// sensible defaults by Do.
type Policy struct {
	MaxAttempts int           // inclusive of the first; 0 means default (3)
	Base        time.Duration // default 500ms
	Cap         time.Duration // default 8s — caps the exponential term
	Ceiling     time.Duration // default 60s — absolute cap; also bounds Retry-After
	Jitter      float64       // 0 means none, downward only; Belai's policies use 0.25
	Rand        func() float64
	Sleep       func(context.Context, time.Duration) error
}

// DefaultRateLimitRetryAfter is the low-pressure backoff used when a provider
// signals rate limiting (HTTP 429 or rate-limit text in the error body) but
// does not supply a Retry-After header. One minute is conservative enough to
// clear most per-minute inference limits while still being bounded by the
// policy Ceiling.
const DefaultRateLimitRetryAfter = 60 * time.Second

// WithDefaults returns a copy of p with every zero field replaced by its
// default, including the Rand and Sleep hooks. Do and Delay normalise
// internally; any caller that reads Policy fields directly must normalise
// first, or a zero-valued Rand/Sleep panics on call.
func (p Policy) WithDefaults() Policy {
	p.defaults()
	return p
}

func (p *Policy) defaults() {
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = 3
	}
	if p.Base <= 0 {
		p.Base = 500 * time.Millisecond
	}
	if p.Cap <= 0 {
		p.Cap = 8 * time.Second
	}
	if p.Ceiling <= 0 {
		p.Ceiling = 60 * time.Second
	}
	if p.Jitter < 0 {
		p.Jitter = 0
	}
	if p.Jitter > 1 {
		p.Jitter = 1
	}
	if p.Rand == nil {
		p.Rand = rand.Float64
	}
	if p.Sleep == nil {
		p.Sleep = func(ctx context.Context, d time.Duration) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d):
				return nil
			}
		}
	}
}

// Delay returns the backoff for a given retry index, honoring an explicit
// Retry-After first and falling back to exponential jitter. retryIndex starts
// at 0 for the first retry (i.e. the second overall attempt).
func (p Policy) Delay(retryIndex int, retryAfter time.Duration, rnd float64) time.Duration {
	p.defaults()
	if retryAfter > 0 {
		if retryAfter > p.Ceiling {
			return p.Ceiling
		}
		return retryAfter
	}
	exp := float64(p.Base) * math.Pow(2, float64(retryIndex))
	if exp > float64(p.Cap) {
		exp = float64(p.Cap)
	}
	jitter := 1.0 - p.Jitter*rnd
	if jitter < 0 {
		jitter = 0
	}
	d := time.Duration(exp * jitter)
	if d <= 0 {
		d = p.Base
	}
	return d
}

// ParseRetryAfter parses a Retry-After header value as either seconds or an
// HTTP-date. Garbage or negative values return 0 so the caller falls back to
// exponential backoff without failing the turn.
func ParseRetryAfter(header string, now time.Time) time.Duration {
	if header == "" {
		return 0
	}
	if n, err := strconv.Atoi(header); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(header); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// Classifier converts an error into a Verdict.
type Classifier interface {
	Classify(error) Verdict
}

// FuncClassifier adapts a plain function to the Classifier interface.
type FuncClassifier func(error) Verdict

// Classify implements Classifier.
func (f FuncClassifier) Classify(err error) Verdict { return f(err) }

// ClassifyStatus maps an HTTP status code to a Verdict. It is exported so callers
// can build custom classifiers without re-implementing the status-code table.
func ClassifyStatus(status int) Verdict {
	switch status {
	case http.StatusTooManyRequests:
		return Verdict{Class: ClassRetryable, Reason: fmt.Sprintf("provider returned %d", status)}
	case http.StatusRequestTimeout, http.StatusConflict:
		return Verdict{Class: ClassRetryable, Reason: fmt.Sprintf("provider returned %d", status)}
	}
	if status >= 500 {
		return Verdict{Class: ClassRetryable, Reason: fmt.Sprintf("provider returned %d", status)}
	}
	return Verdict{Class: ClassFatal, Reason: fmt.Sprintf("provider returned %d", status)}
}

var (
	denyRE      = regexp.MustCompile(`(?i)quota|billing|usage.limit|insufficient_quota|payment|CARD_|invalid_api_key|invalid_auth`)
	overflowRE  = regexp.MustCompile(`(?i)context.length|context.too.long|maximum.context|token.?limit|too many tokens|context length exceeded`)
	allowRE     = regexp.MustCompile(`(?i)ended without|stream reset|connection reset|connection refused|read response|unexpected EOF|broken pipe|timeout awaiting response headers|no such host`)
	rateLimitRE = regexp.MustCompile(`(?i)rate.?limit|too.?many.?requests|throttl|requests?\s+per\s+(min|minute|sec|second|hour)|inferencerequestpermin|over.?capacity|ENHANCE_YOUR_CALM`)
	// transportRE covers HTTP/2 and net/http failures the standard library
	// does not export as types (the bundled h2 errors are unexported), so
	// text is the only signal. Every entry is a peer or connection fault that
	// the next attempt on a fresh stream can clear.
	transportRE = regexp.MustCompile(`(?i)stream error|PROTOCOL_ERROR|INTERNAL_ERROR|REFUSED_STREAM|GOAWAY|http2: client connection lost|server closed idle connection|use of closed network connection|TLS handshake timeout|i/o timeout|stream idle timeout|closed without a done chunk|connection closed before|overloaded`)
)

// DefaultClassifier is the denylist-first classifier used by Belai's provider
// retry layer. Unrecognised errors resolve to ClassFatal.
type DefaultClassifier struct{}

// Classify implements the retry classification order from the design doc:
//  1. context errors → ClassAborted
//  2. typed transport faults (reset, EOF, timeout) → ClassRetryable
//  3. denylist text → ClassFatal
//  4. overflow text → ClassOverflow
//  5. explicit Retry-After → ClassRetryable
//  6. rate-limit signal without Retry-After → ClassRetryable with low-pressure default
//  7. status code / retryable or transport text → ClassRetryable
//  8. default → ClassFatal
//
// For a transport error (*url.Error) the text steps match the underlying
// cause only, so words in the request URL never decide the verdict.
func (DefaultClassifier) Classify(err error) Verdict {
	if err == nil {
		return Verdict{Class: ClassFatal, Reason: "nil error"}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return Verdict{Class: ClassAborted, Reason: "request cancelled"}
	}
	if transportFault(err) {
		return Verdict{Class: ClassRetryable, Reason: "transport error"}
	}

	msg := err.Error()
	var uerr *url.Error
	if errors.As(err, &uerr) && uerr.Err != nil {
		msg = uerr.Err.Error()
	}
	if denyRE.MatchString(msg) {
		return Verdict{Class: ClassFatal, Reason: "provider denied the request"}
	}
	if overflowRE.MatchString(msg) {
		return Verdict{Class: ClassOverflow, Reason: "context too long"}
	}

	var ra RetryAfterer
	if errors.As(err, &ra) {
		if retryAfter := ra.RetryAfter(); retryAfter > 0 {
			return Verdict{Class: ClassRetryable, RetryAfter: retryAfter, Reason: "retry-after supplied"}
		}
	}

	status := 0
	var sc StatusCoder
	if errors.As(err, &sc) {
		status = sc.StatusCode()
	}
	// Low-pressure rate-limit retry: when the provider says 429 or the body
	// contains a rate-limit signal but omits Retry-After, default to a longer
	// wait instead of the usual exponential storm. This keeps Belai civil to
	// providers with per-minute limits (common for local inference and small
	// model endpoints) without requiring users to configure a ceiling.
	if status == http.StatusTooManyRequests || rateLimitRE.MatchString(msg) {
		return Verdict{Class: ClassRetryable, RetryAfter: DefaultRateLimitRetryAfter, Reason: "rate limit; using low-pressure default backoff"}
	}

	if status != 0 {
		return ClassifyStatus(status)
	}
	if allowRE.MatchString(msg) || transportRE.MatchString(msg) {
		return Verdict{Class: ClassRetryable, Reason: "connection/stream reset"}
	}
	return Verdict{Class: ClassFatal}
}

// transportFault reports whether err is a typed connection-level failure that
// a fresh attempt can clear. A bare io.EOF only counts inside a *url.Error,
// where it means the server closed a pooled connection under the request.
func transportFault(err error) bool {
	if errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.EPIPE) {
		return true
	}
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return true
	}
	var uerr *url.Error
	return errors.As(err, &uerr) && errors.Is(uerr.Err, io.EOF)
}

// Do executes attempt until it succeeds, the classifier says the error is not
// retryable, the context is cancelled, or MaxAttempts is reached. onRetry is
// called before each backoff sleep (nil is safe).
func Do[T any](ctx context.Context, p Policy, c Classifier, attempt func(context.Context) (T, error), onRetry func(Attempt)) (T, error) {
	p.defaults()
	var zero T
	retryIndex := 0
	for {
		v, err := attempt(ctx)
		if err == nil {
			return v, nil
		}
		verdict := c.Classify(err)
		if verdict.Class == ClassAborted || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return zero, err
		}
		if verdict.Class != ClassRetryable {
			return zero, err
		}
		// MaxAttempts is inclusive of the first attempt.
		if retryIndex+1 >= p.MaxAttempts {
			return zero, err
		}
		delay := p.Delay(retryIndex, verdict.RetryAfter, p.Rand())
		if onRetry != nil {
			onRetry(Attempt{Attempt: retryIndex + 2, Max: p.MaxAttempts, Delay: delay, Reason: verdict.Reason})
		}
		if err := p.Sleep(ctx, delay); err != nil {
			return zero, err
		}
		retryIndex++
	}
}
