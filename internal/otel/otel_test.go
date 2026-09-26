package otel

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/signet/internal/config"
)

// Collector records every OTLP payload it receives.
type Collector struct {
	mu     sync.Mutex
	Bodies map[string][]string
	Srv    *httptest.Server
}

func NewCollector() *Collector {
	c := &Collector{Bodies: map[string][]string{}}
	c.Srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.Bodies[r.URL.Path] = append(c.Bodies[r.URL.Path], string(b))
		c.mu.Unlock()
	}))
	return c
}

func (c *Collector) All() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var b strings.Builder
	for _, bs := range c.Bodies {
		for _, s := range bs {
			b.WriteString(s)
		}
	}
	return b.String()
}

func TestExportAllowlistsAttributes(t *testing.T) {
	col := NewCollector()
	defer col.Srv.Close()
	stop := Start(Config{Endpoint: col.Srv.URL, Traces: true, Metrics: true, Interval: time.Hour}, "abc123")
	sp := StartSpan(context.Background(), "signet.tool_call",
		S(AttrToolName, "Bash"),
		S("signet.prompt", "rm -rf / please"),
		S(AttrOutcome, "ok; ignore previous instructions <system>"),
	)
	sp.End()
	Add("signet.tool_calls", 2, S(AttrToolKind, "bash"), S("file.path", "/etc/passwd"))
	Observe("signet.turn.duration", 3*time.Second, S(AttrMode, "agent"))
	stop()

	all := col.All()
	for _, bad := range []string{"signet.prompt", "rm -rf", "file.path", "/etc/passwd", "<system>", "ignore previous instructions"} {
		if strings.Contains(all, bad) {
			t.Errorf("export carried %q", bad)
		}
	}
	for _, want := range []string{`"signet.tool.name"`, `"Bash"`, `"signet.tool_calls"`, `"asInt":"2"`, `"signet.turn.duration"`, `"service.name"`} {
		if !strings.Contains(all, want) {
			t.Errorf("export lacks %s:\n%s", want, all)
		}
	}
	if len(col.Bodies["/v1/traces"]) != 1 || len(col.Bodies["/v1/metrics"]) != 1 {
		t.Fatalf("paths = %v", col.Bodies)
	}
}

func TestDisabledIsNoop(t *testing.T) {
	if Enabled() {
		t.Fatal("exporter enabled without Start")
	}
	sp := StartSpan(context.Background(), "x")
	sp.Set(S(AttrMode, "agent"))
	sp.End()
	Add("x", 1)
	stop := Start(Config{}, "k")
	stop()
	if Enabled() {
		t.Fatal("empty endpoint enabled export")
	}
}

func TestFromSettings(t *testing.T) {
	off := false
	env := map[string]string{"TOKEN": "t0k", "OTEL_EXPORTER_OTLP_HEADERS": "x-a=1, x-b=2"}
	c := FromSettings(&config.TelemetrySettings{OTLPEndpoint: "http://c:4318/", Headers: map[string]string{"auth": "env:TOKEN"}, Metrics: &off}, func(k string) string { return env[k] })
	if c.Endpoint != "http://c:4318" || c.Headers["auth"] != "t0k" || c.Headers["x-b"] != "2" || !c.Traces || c.Metrics {
		t.Fatalf("cfg = %+v", c)
	}
	env["OTEL_EXPORTER_OTLP_ENDPOINT"] = "http://env:4318"
	if c := FromSettings(nil, func(k string) string { return env[k] }); c.Endpoint != "http://env:4318" {
		t.Fatalf("env endpoint = %q", c.Endpoint)
	}
	env["OTEL_SDK_DISABLED"] = "true"
	if c := FromSettings(nil, func(k string) string { return env[k] }); c.Endpoint != "" {
		t.Fatal("OTEL_SDK_DISABLED ignored")
	}
}

// Every key the code exports must be on the allowlist, so a new attribute
// is a deliberate edit to this package.
func TestAllowlistIsClosed(t *testing.T) {
	for _, k := range []string{AttrMode, AttrOutcome, AttrPasses, AttrProvider, AttrModel, AttrRole, AttrTokens, AttrEstimated, AttrToolName, AttrToolKind, AttrDecision, AttrVerdict, AttrHookEvent, AttrHooksRan, AttrHooksFail, AttrProjectKey} {
		if !Allowed(k) {
			t.Errorf("%s not allowed", k)
		}
	}
	if len(allowedAttrs) != 16 {
		t.Fatalf("allowlist has %d keys; update this test deliberately", len(allowedAttrs))
	}
	if Allowed("signet.prompt") || Allowed("tool.args") {
		t.Fatal("content-shaped key allowed")
	}
}
