package httpclient

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/trace"
)

// TestTracingTransportRecordsMetadataOnly checks one record per request with
// the endpoint, the model from the body, byte counts and the status — and
// never the body or a header value.
func TestTracingTransportRecordsMetadataOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"secret-response":"x"}`)
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "trace.jsonl")
	w, err := trace.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c := &http.Client{Transport: &tracingTransport{next: http.DefaultTransport, w: w}}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m-1","messages":[{"content":"secret-prompt"}]}`))
	req.Header.Set("authorization", "Bearer secret-key")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	_ = w.Close()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret-prompt", "secret-key", "secret-response"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("trace leaked %q: %s", secret, raw)
		}
	}
	var r trace.Record
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &r); err != nil {
		t.Fatalf("trace line: %v (%s)", err, raw)
	}
	if r.Phase != "http" || r.Event != "request" || r.Model != "m-1" || r.Verdict != "200" {
		t.Fatalf("record = %+v", r)
	}
	if !strings.Contains(r.Detail, "POST ") || !strings.Contains(r.Detail, "/v1/chat/completions") || !strings.Contains(r.Detail, "resp=23B") {
		t.Fatalf("detail = %q", r.Detail)
	}
}

// TestTracingTransportRecordsTransportErrors checks a request that never gets
// a response still leaves a record naming the error.
func TestTracingTransportRecordsTransportErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	w, err := trace.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c := &http.Client{Transport: &tracingTransport{next: http.DefaultTransport, w: w}}
	if _, err := c.Get("http://127.0.0.1:1/unreachable"); err == nil {
		t.Fatal("expected a transport error")
	}
	_ = w.Close()
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"verdict":"error"`) || !strings.Contains(string(raw), "err=") {
		t.Fatalf("no error record: %s", raw)
	}
}

// TestWithTracingIsOffWithoutTraceEnv checks an untraced session keeps the
// bare transport.
func TestWithTracingIsOffWithoutTraceEnv(t *testing.T) {
	t.Setenv("BELAI_TRACE", "")
	rt := http.DefaultTransport
	if got := withTracing(rt); got != rt {
		t.Fatalf("withTracing wrapped the transport with tracing off: %T", got)
	}
}

// TestForBlockingSwapsOnlyTheSharedClient checks non-streaming model calls get
// the long header bound, while any other client is left alone.
func TestForBlockingSwapsOnlyTheSharedClient(t *testing.T) {
	if ForBlocking(Default()) != blockingClient || ForBlocking(nil) != blockingClient {
		t.Fatal("the shared client was not swapped for the blocking client")
	}
	own := &http.Client{}
	if ForBlocking(own) != own {
		t.Fatal("a caller's own client was replaced")
	}
	if tr, ok := unwrapTransport(blockingClient.Transport); !ok || tr.ResponseHeaderTimeout != BlockingResponseTimeout {
		t.Fatalf("blocking transport header bound = %v, want %v", tr.ResponseHeaderTimeout, BlockingResponseTimeout)
	}
	if tr, ok := unwrapTransport(sharedClient.Transport); !ok || tr.ResponseHeaderTimeout != ResponseHeaderTimeout {
		t.Fatalf("shared transport header bound = %v, want %v", tr.ResponseHeaderTimeout, ResponseHeaderTimeout)
	}
}

func unwrapTransport(rt http.RoundTripper) (*http.Transport, bool) {
	if tt, ok := rt.(*tracingTransport); ok {
		rt = tt.next
	}
	tr, ok := rt.(*http.Transport)
	return tr, ok
}
