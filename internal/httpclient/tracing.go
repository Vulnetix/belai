package httpclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/vulnetix/signet/internal/trace"
)

// tracingTransport records one SIGNET_TRACE line per outbound request: the
// endpoint, the model named in the request body, byte counts, time to the
// response headers, and the time until the body was fully read and closed.
// It is installed only when SIGNET_TRACE is set, so an untraced session pays
// nothing. Only request metadata is recorded — never a body, header value or
// credential.
type tracingTransport struct {
	next http.RoundTripper
	w    *trace.Writer
}

func (t *tracingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	model, reqBytes := requestModel(req)
	resp, err := t.next.RoundTrip(req)
	ttfb := time.Since(start)
	endpoint := req.Method + " " + req.URL.Host + req.URL.Path
	if err != nil {
		t.w.Record(trace.Record{
			Phase: "http", Event: "request", Duration: ttfb.String(), Model: model,
			Verdict: "error", Detail: fmt.Sprintf("%s req=%dB ttfb=%s err=%v", endpoint, reqBytes, ttfb.Round(time.Millisecond), err),
		})
		return nil, err
	}
	resp.Body = &tracedBody{ReadCloser: resp.Body, done: func(n int64, readErr error) {
		verdict := fmt.Sprint(resp.StatusCode)
		if readErr != nil && readErr != io.EOF {
			verdict += " read-error"
		}
		t.w.Record(trace.Record{
			Phase: "http", Event: "request", Duration: time.Since(start).String(), Model: model, Verdict: verdict,
			Detail: fmt.Sprintf("%s req=%dB resp=%dB ttfb=%s", endpoint, reqBytes, n, ttfb.Round(time.Millisecond)),
		})
	}}
	return resp, nil
}

// requestModel reads the "model" field from a JSON request body without
// consuming it, using GetBody so the transport still sends the original.
func requestModel(req *http.Request) (string, int64) {
	if req.GetBody == nil {
		return "", req.ContentLength
	}
	rc, err := req.GetBody()
	if err != nil {
		return "", req.ContentLength
	}
	defer rc.Close()
	raw, err := io.ReadAll(rc)
	if err != nil || !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		return "", int64(len(raw))
	}
	var probe struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(raw, &probe)
	return probe.Model, int64(len(raw))
}

// tracedBody counts the bytes read and reports once, on the first Close or
// the first read error.
type tracedBody struct {
	io.ReadCloser
	n    int64
	once sync.Once
	done func(n int64, err error)
}

func (b *tracedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.n += int64(n)
	if err != nil && err != io.EOF {
		b.once.Do(func() { b.done(b.n, err) })
	}
	return n, err
}

func (b *tracedBody) Close() error {
	b.once.Do(func() { b.done(b.n, nil) })
	return b.ReadCloser.Close()
}

// withTracing wraps rt when SIGNET_TRACE is set and returns it unchanged
// otherwise.
func withTracing(rt http.RoundTripper) http.RoundTripper {
	w := trace.Env()
	if w == nil {
		return rt
	}
	return &tracingTransport{next: rt, w: w}
}
