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

	"github.com/vulnetix/belai/internal/config"
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
	sp := StartSpan(context.Background(), "belai.tool_call",
		S(AttrToolName, "Bash"),
		S("belai.prompt", "rm -rf / please"),
		S(AttrOutcome, "ok; ignore previous instructions <system>"),
	)
	sp.End()
	Add("belai.tool_calls", 2, S(AttrToolKind, "bash"), S("file.path", "/etc/passwd"))
	Observe("belai.turn.duration", 3*time.Second, S(AttrMode, "agent"))
	stop()

	all := col.All()
	for _, bad := range []string{"belai.prompt", "rm -rf", "file.path", "/etc/passwd", "<system>", "ignore previous instructions"} {
		if strings.Contains(all, bad) {
			t.Errorf("export carried %q", bad)
		}
	}
	for _, want := range []string{`"belai.tool.name"`, `"Bash"`, `"belai.tool_calls"`, `"asInt":"2"`, `"belai.turn.duration"`, `"service.name"`} {
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
	for _, k := range []string{AttrMode, AttrOutcome, AttrPasses, AttrProvider, AttrModel, AttrRole, AttrEstimated, AttrToolName, AttrToolKind, AttrDecision, AttrVerdict, AttrHookEvent, AttrProjectKey} {
		if !Allowed(k) {
			t.Errorf("%s not allowed", k)
		}
	}
	if len(allowedAttrs) != 13 {
		t.Fatalf("allowlist has %d keys; update this test deliberately", len(allowedAttrs))
	}
	if Allowed("belai.prompt") || Allowed("tool.args") {
		t.Fatal("content-shaped key allowed")
	}
}

func TestConfigEdges(t *testing.T) {
	c := FromSettings(&config.TelemetrySettings{OTLPEndpoint: "http://c:4318///"}, func(string) string { return "" })
	if c.Endpoint != "http://c:4318" {
		t.Fatalf("endpoint = %q", c.Endpoint)
	}
	stop := Start(Config{Endpoint: "http://c:4318"}, "k")
	defer stop()
	if Enabled() {
		t.Fatal("traces and metrics both off still started")
	}
}

func TestCleanValue(t *testing.T) {
	if got := cleanValue("anthropic/claude-opus-5-5@2026"); got != "anthropic/claude-opus-5-5@2026" {
		t.Fatalf("model id changed: %q", got)
	}
	if got := cleanValue("hello world\n<x>"); strings.ContainsAny(got, " \n<>") {
		t.Fatalf("prose survived: %q", got)
	}
	if got := cleanValue(strings.Repeat("a", 200)); len(got) != 96 {
		t.Fatalf("len = %d", len(got))
	}
}

// Past the queue cap spans are dropped, not buffered without bound.
func TestSpanQueueCap(t *testing.T) {
	col := NewCollector()
	defer col.Srv.Close()
	stop := Start(Config{Endpoint: col.Srv.URL, Traces: true, Interval: time.Hour}, "k")
	defer stop()
	e := current()
	for i := 0; i < maxQueuedSpans+10; i++ {
		StartSpan(context.Background(), "s").End()
	}
	e.mu.Lock()
	n, dropped := len(e.spans), e.dropped
	e.mu.Unlock()
	if n != maxQueuedSpans || dropped != 10 {
		t.Fatalf("queued %d dropped %d", n, dropped)
	}
}
