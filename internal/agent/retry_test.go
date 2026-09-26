package agent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/resilience"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
)

// midStreamResetBody yields one SSE chunk and then fails the read the way an
// HTTP/2 body does when the peer resets the stream.
type midStreamResetBody struct{ sent bool }

func (b *midStreamResetBody) Read(p []byte) (int, error) {
	if !b.sent {
		b.sent = true
		return copy(p, "data: {\"choices\":[{\"delta\":{\"content\":\"par\"}}]}\n\n"), nil
	}
	return 0, errors.New("stream error: stream ID 85; INTERNAL_ERROR; received from peer")
}

func (b *midStreamResetBody) Close() error { return nil }

// resetOnceTransport resets the first chat stream mid-body and serves a
// complete stream on the next one. Other requests (the nonce fetch) get 404.
type resetOnceTransport struct{ calls int }

func (t *resetOnceTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	}
	t.calls++
	var body io.ReadCloser = &midStreamResetBody{}
	if t.calls > 1 {
		body = io.NopCloser(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"full\"}}]}\n\ndata: [DONE]\n\n"))
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       body,
		Request:    r,
	}, nil
}

// TestStreamTurnRetryRecoversFromMidStreamH2Reset pins the L2 half of the
// gateway PROTOCOL_ERROR fix: a peer stream reset after the first byte is
// past L1's reach, so the turn-level loop must classify it retryable, report
// its real budget, and replay the turn.
func TestStreamTurnRetryRecoversFromMidStreamH2Reset(t *testing.T) {
	tr := &resetOnceTransport{}
	sess, err := NewSession(Options{
		Cfg:      run.Config{Provider: "openai", BaseURL: "https://gateway.example/v1", APIKey: "k", Model: "test"},
		Client:   &http.Client{Transport: tr},
		Registry: tools.NewRegistry(),
		Workdir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	var retries []Event
	got, err := sess.streamTurnRetry(context.Background(), "", []run.Turn{{Role: "user", Content: "hi"}}, true, func(e Event) {
		if e.Kind == EventRetryKind {
			retries = append(retries, e)
		}
	})
	if err != nil {
		t.Fatalf("streamTurnRetry: %v", err)
	}
	if got.Text != "full" {
		t.Fatalf("text = %q, want the retried stream's output", got.Text)
	}
	if tr.calls != 2 || len(retries) != 1 {
		t.Fatalf("calls=%d retries=%d, want 2 and 1", tr.calls, len(retries))
	}
	if retries[0].RetryAttempt != 2 || retries[0].RetryMax != 3 {
		t.Fatalf("retry event = %d/%d, want 2/3", retries[0].RetryAttempt, retries[0].RetryMax)
	}
}

type iotestErrReader struct{ err error }

func (r iotestErrReader) Read([]byte) (int, error) { return 0, r.err }

// rateLimitStreamTransport fails every chat stream mid-body with rate-limit
// text and no Retry-After.
type rateLimitStreamTransport struct{}

func (rateLimitStreamTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(iotestErrReader{errors.New("rate limit exceeded")}),
		Request:    r,
	}, nil
}

// TestStreamTurnRetryUsesVerdictRateLimitDelay pins that L2 takes its delay
// from the classifier verdict: a rate-limit failure without Retry-After waits
// the low-pressure default, not the 0.5 s exponential base.
func TestStreamTurnRetryUsesVerdictRateLimitDelay(t *testing.T) {
	sess, err := NewSession(Options{
		Cfg:      run.Config{Provider: "openai", BaseURL: "https://gateway.example/v1", APIKey: "k", Model: "test"},
		Client:   &http.Client{Transport: rateLimitStreamTransport{}},
		Registry: tools.NewRegistry(),
		Workdir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var retry *Event
	_, _ = sess.streamTurnRetry(ctx, "", []run.Turn{{Role: "user", Content: "hi"}}, true, func(e Event) {
		if e.Kind == EventRetryKind && retry == nil {
			retry = &e
			cancel() // end the backoff sleep immediately
		}
	})
	if retry == nil {
		t.Fatal("rate-limit failure was not retried")
	}
	if retry.RetryDelay != resilience.DefaultRateLimitRetryAfter {
		t.Fatalf("delay = %v, want %v", retry.RetryDelay, resilience.DefaultRateLimitRetryAfter)
	}
}
