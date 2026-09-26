package tools

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/calltrace"
	"github.com/vulnetix/belai/internal/version"
)

// captureTransport records the last request and answers with a fixed body.
type captureTransport struct {
	req  *http.Request
	ct   string
	body string
}

func (c *captureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.req = r
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{c.ct}},
		Body:       io.NopCloser(strings.NewReader(c.body)),
		Request:    r,
	}, nil
}

func traceCtx(tool string) context.Context {
	return calltrace.WithTool(calltrace.WithSession(context.Background(), "sess-abc"), tool, "call_1")
}

func assertTraceHeaders(t *testing.T, h http.Header, tool string) {
	t.Helper()
	want := map[string]string{
		"User-Agent":                  version.UserAgent(),
		calltrace.HeaderSessionID:     "sess-abc",
		calltrace.HeaderTool:          tool,
		calltrace.HeaderToolCallID:    "call_1",
		calltrace.HeaderClientVersion: version.Version,
	}
	for k, v := range want {
		if got := h.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if !strings.HasPrefix(h.Get(calltrace.HeaderTraceparent), "00-") {
		t.Errorf("traceparent = %q", h.Get(calltrace.HeaderTraceparent))
	}
}

func TestWebFetchSendsTraceHeaders(t *testing.T) {
	ct := &captureTransport{ct: "text/plain", body: "ok"}
	wf := &WebFetch{Client: &http.Client{Transport: ct}}
	if _, err := wf.Execute(traceCtx("WebFetch"), map[string]any{"url": "http://192.0.2.1/"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	assertTraceHeaders(t, ct.req.Header, "WebFetch")
}

func TestWebSearchSendsTraceHeaders(t *testing.T) {
	ct := &captureTransport{ct: "application/json", body: "{}"}
	ws := &WebSearch{Client: &http.Client{Transport: ct}, Endpoint: "https://search.example/api"}
	if _, err := ws.Execute(traceCtx("WebSearch"), map[string]any{"query": "q"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	assertTraceHeaders(t, ct.req.Header, "WebSearch")
}

func TestBashExportsTraceEnv(t *testing.T) {
	b := &Bash{Root: t.TempDir(), Timeout: 5 * time.Second}
	res, err := b.Execute(traceCtx("Bash"), map[string]any{"command": "echo $BELAI:$BELAI_SESSION_ID:$BELAI_TOOL:$BELAI_TOOL_CALL_ID"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "1:sess-abc:Bash:call_1") {
		t.Fatalf("env not exported: %q", res.Content)
	}
}
