// Package otel exports Signet's traces and metrics to an OpenTelemetry
// collector over OTLP/HTTP with JSON encoding. It carries facts about a
// session, never its content: every attribute key comes from a fixed
// allowlist and every string value is reduced to an identifier, so no
// prompt, reply, argument, output, path or URL can leave through it.
// Export is batched on a background goroutine and drops data rather than
// slow a turn.
package otel

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/signet/internal/calltrace"
	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/version"
)

// Attribute keys. Nothing else is ever exported.
const (
	AttrMode       = "signet.mode"
	AttrOutcome    = "signet.outcome"
	AttrPasses     = "signet.passes"
	AttrProvider   = "signet.provider"
	AttrModel      = "signet.model"
	AttrRole       = "signet.role"
	AttrEstimated  = "signet.tokens.estimated"
	AttrToolName   = "signet.tool.name"
	AttrToolKind   = "signet.tool.kind"
	AttrDecision   = "signet.decision"
	AttrVerdict    = "signet.verdict"
	AttrHookEvent  = "signet.hook.event"
	AttrProjectKey = "signet.project"
)

var allowedAttrs = map[string]bool{
	AttrMode: true, AttrOutcome: true, AttrPasses: true, AttrProvider: true,
	AttrModel: true, AttrRole: true, AttrEstimated: true,
	AttrToolName: true, AttrToolKind: true, AttrDecision: true, AttrVerdict: true,
	AttrHookEvent: true, AttrProjectKey: true,
}

// Allowed reports whether key may be exported.
func Allowed(key string) bool { return allowedAttrs[key] }

var unsafeValue = regexp.MustCompile(`[^A-Za-z0-9._:/@+-]+`)

// cleanValue keeps an identifier-shaped value only: model ids, tool names,
// sentinels. Anything prose-shaped is squeezed to identifier characters and
// capped, so it cannot carry content.
func cleanValue(v string) string {
	v = unsafeValue.ReplaceAllString(v, "_")
	if len(v) > 96 {
		v = v[:96]
	}
	return v
}

// Attr is one attribute.
type Attr struct {
	Key string
	Str string
	Int int64
	isI bool
}

// S is a string attribute.
func S(k, v string) Attr { return Attr{Key: k, Str: v} }

// I is an integer attribute.
func I(k string, v int64) Attr { return Attr{Key: k, Int: v, isI: true} }

// Config is the resolved export configuration.
type Config struct {
	Endpoint string
	Headers  map[string]string
	Traces   bool
	Metrics  bool
	// Interval is the export period (default 10s).
	Interval time.Duration
	Client   *http.Client
}

// FromSettings resolves the configuration from the global settings and the
// standard OTEL_* environment. Empty Endpoint means export is off.
func FromSettings(s *config.TelemetrySettings, getenv func(string) string) Config {
	c := Config{Traces: true, Metrics: true}
	if s != nil {
		c.Endpoint = s.OTLPEndpoint
		c.Headers = map[string]string{}
		for k, v := range s.Headers {
			c.Headers[k] = expand(v, getenv)
		}
		if s.Traces != nil {
			c.Traces = *s.Traces
		}
		if s.Metrics != nil {
			c.Metrics = *s.Metrics
		}
	}
	if v := getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); v != "" {
		c.Endpoint = v
	}
	if v := getenv("OTEL_EXPORTER_OTLP_HEADERS"); v != "" {
		if c.Headers == nil {
			c.Headers = map[string]string{}
		}
		for _, kv := range strings.Split(v, ",") {
			if k, val, ok := strings.Cut(kv, "="); ok {
				c.Headers[strings.TrimSpace(k)] = strings.TrimSpace(val)
			}
		}
	}
	if strings.EqualFold(getenv("OTEL_SDK_DISABLED"), "true") {
		c.Endpoint = ""
	}
	c.Endpoint = strings.TrimRight(c.Endpoint, "/")
	return c
}

func expand(v string, getenv func(string) string) string {
	if name, ok := strings.CutPrefix(v, "env:"); ok {
		return getenv(name)
	}
	return v
}

// Exporter batches spans and metric points.
type Exporter struct {
	cfg      Config
	resource []Attr

	mu      sync.Mutex
	spans   []spanData
	sums    map[string]*sumPoint
	hists   map[string]*histPoint
	stop    chan struct{}
	done    chan struct{}
	dropped int
}

const maxQueuedSpans = 4096

type spanData struct {
	traceID, spanID, name string
	start, end            time.Time
	attrs                 []Attr
	failed                bool
}

type sumPoint struct {
	name  string
	attrs []Attr
	value int64
}

type histPoint struct {
	name   string
	attrs  []Attr
	count  uint64
	sum    float64
	bucket []uint64
}

// histogram bounds for turn durations, in seconds.
var durationBounds = []float64{1, 5, 15, 30, 60, 120, 300, 600, 1800}

var (
	activeMu sync.RWMutex
	active   *Exporter
)

// Start begins exporting and installs the exporter process-wide. It returns
// a shutdown that flushes once. An empty endpoint returns a no-op.
func Start(cfg Config, projectKey string) func() {
	if cfg.Endpoint == "" || (!cfg.Traces && !cfg.Metrics) {
		return func() {}
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 10 * time.Second
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 10 * time.Second}
	}
	e := &Exporter{
		cfg:      cfg,
		resource: []Attr{S("service.name", "signet"), S("service.version", version.Version), S(AttrProjectKey, projectKey)},
		sums:     map[string]*sumPoint{},
		hists:    map[string]*histPoint{},
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go e.loop()
	activeMu.Lock()
	active = e
	activeMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			activeMu.Lock()
			if active == e {
				active = nil
			}
			activeMu.Unlock()
			close(e.stop)
			<-e.done
		})
	}
}

func current() *Exporter {
	activeMu.RLock()
	defer activeMu.RUnlock()
	return active
}

// Enabled reports whether an exporter is running.
func Enabled() bool { return current() != nil }

func (e *Exporter) loop() {
	defer close(e.done)
	t := time.NewTicker(e.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			e.flush()
		case <-e.stop:
			e.flush()
			return
		}
	}
}

// Span is an in-flight span. A nil Span is a no-op.
type Span struct {
	e    *Exporter
	d    spanData
	once sync.Once
}

// StartSpan starts a span in ctx's trace (the session's), or a fresh trace
// when ctx carries none. It returns nil when export is off.
func StartSpan(ctx context.Context, name string, attrs ...Attr) *Span {
	e := current()
	if e == nil || !e.cfg.Traces {
		return nil
	}
	tid := calltrace.TraceID(ctx)
	if tid == "" {
		tid = randHex(16)
	}
	sid := calltrace.SpanID(ctx)
	if sid == "" {
		sid = randHex(8)
	}
	return &Span{e: e, d: spanData{traceID: tid, spanID: sid, name: name, start: time.Now(), attrs: attrs}}
}

// Set adds attributes.
func (s *Span) Set(attrs ...Attr) {
	if s != nil {
		s.d.attrs = append(s.d.attrs, attrs...)
	}
}

// Fail marks the span as an error.
func (s *Span) Fail() {
	if s != nil {
		s.d.failed = true
	}
}

// End finishes the span and queues it.
func (s *Span) End() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		s.d.end = time.Now()
		s.e.mu.Lock()
		defer s.e.mu.Unlock()
		if len(s.e.spans) >= maxQueuedSpans {
			s.e.dropped++
			return
		}
		s.e.spans = append(s.e.spans, s.d)
	})
}

// Add increments a counter.
func Add(name string, v int64, attrs ...Attr) {
	e := current()
	if e == nil || !e.cfg.Metrics {
		return
	}
	k := seriesKey(name, attrs)
	e.mu.Lock()
	defer e.mu.Unlock()
	p := e.sums[k]
	if p == nil {
		p = &sumPoint{name: name, attrs: attrs}
		e.sums[k] = p
	}
	p.value += v
}

// Observe records a duration in the histogram name, in seconds.
func Observe(name string, d time.Duration, attrs ...Attr) {
	e := current()
	if e == nil || !e.cfg.Metrics {
		return
	}
	k := seriesKey(name, attrs)
	sec := d.Seconds()
	e.mu.Lock()
	defer e.mu.Unlock()
	h := e.hists[k]
	if h == nil {
		h = &histPoint{name: name, attrs: attrs, bucket: make([]uint64, len(durationBounds)+1)}
		e.hists[k] = h
	}
	h.count++
	h.sum += sec
	i := sort.SearchFloat64s(durationBounds, sec)
	h.bucket[i]++
}

func seriesKey(name string, attrs []Attr) string {
	var b strings.Builder
	b.WriteString(name)
	for _, a := range encodeAttrs(attrs) {
		b.WriteString("|" + a.Key + "=" + a.Value.String())
	}
	return b.String()
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// OTLP JSON shapes.

type kv struct {
	Key   string   `json:"key"`
	Value anyValue `json:"value"`
}

type anyValue struct {
	StringValue *string `json:"stringValue,omitempty"`
	IntValue    *string `json:"intValue,omitempty"`
}

func (v anyValue) String() string {
	if v.StringValue != nil {
		return *v.StringValue
	}
	if v.IntValue != nil {
		return *v.IntValue
	}
	return ""
}

// encodeAttrs is the single point attributes leave through: unknown keys are
// dropped and string values reduced to identifiers.
func encodeAttrs(attrs []Attr) []kv {
	out := make([]kv, 0, len(attrs))
	for _, a := range attrs {
		if !allowedAttrs[a.Key] && a.Key != "service.name" && a.Key != "service.version" {
			continue
		}
		if a.isI {
			s := strconv.FormatInt(a.Int, 10)
			out = append(out, kv{Key: a.Key, Value: anyValue{IntValue: &s}})
			continue
		}
		s := cleanValue(a.Str)
		out = append(out, kv{Key: a.Key, Value: anyValue{StringValue: &s}})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func nanos(t time.Time) string { return strconv.FormatInt(t.UnixNano(), 10) }

func (e *Exporter) flush() {
	e.mu.Lock()
	spans := e.spans
	e.spans = nil
	var sums []sumPoint
	for _, p := range e.sums {
		sums = append(sums, *p)
	}
	var hists []histPoint
	for _, h := range e.hists {
		cp := *h
		cp.bucket = append([]uint64(nil), h.bucket...)
		hists = append(hists, cp)
	}
	e.mu.Unlock()

	scope := map[string]any{"name": "signet", "version": version.Version}
	res := map[string]any{"attributes": encodeAttrs(e.resource)}
	if e.cfg.Traces && len(spans) > 0 {
		var out []any
		for _, s := range spans {
			sp := map[string]any{
				"traceId": s.traceID, "spanId": s.spanID, "name": s.name, "kind": 1,
				"startTimeUnixNano": nanos(s.start), "endTimeUnixNano": nanos(s.end),
				"attributes": encodeAttrs(s.attrs),
			}
			if s.failed {
				sp["status"] = map[string]any{"code": 2}
			}
			out = append(out, sp)
		}
		e.post("/v1/traces", map[string]any{"resourceSpans": []any{map[string]any{"resource": res, "scopeSpans": []any{map[string]any{"scope": scope, "spans": out}}}}})
	}
	if e.cfg.Metrics && (len(sums) > 0 || len(hists) > 0) {
		now := nanos(time.Now())
		byName := map[string][]any{}
		for _, p := range sums {
			v := strconv.FormatInt(p.value, 10)
			byName[p.name] = append(byName[p.name], map[string]any{"attributes": encodeAttrs(p.attrs), "timeUnixNano": now, "asInt": v})
		}
		var metrics []any
		for name, pts := range byName {
			metrics = append(metrics, map[string]any{"name": name, "sum": map[string]any{"aggregationTemporality": 2, "isMonotonic": true, "dataPoints": pts}})
		}
		hByName := map[string][]any{}
		for _, h := range hists {
			counts := make([]string, len(h.bucket))
			for i, c := range h.bucket {
				counts[i] = strconv.FormatUint(c, 10)
			}
			hByName[h.name] = append(hByName[h.name], map[string]any{
				"attributes": encodeAttrs(h.attrs), "timeUnixNano": now,
				"count": strconv.FormatUint(h.count, 10), "sum": h.sum,
				"bucketCounts": counts, "explicitBounds": durationBounds,
			})
		}
		for name, pts := range hByName {
			metrics = append(metrics, map[string]any{"name": name, "unit": "s", "histogram": map[string]any{"aggregationTemporality": 2, "dataPoints": pts}})
		}
		e.post("/v1/metrics", map[string]any{"resourceMetrics": []any{map[string]any{"resource": res, "scopeMetrics": []any{map[string]any{"scope": scope, "metrics": metrics}}}}})
	}
}

// post sends one payload. Failures are dropped: telemetry never fails a
// session.
func (e *Exporter) post(path string, body any) {
	data, err := json.Marshal(body)
	if err != nil {
		return
	}
	req, err := http.NewRequest(http.MethodPost, e.cfg.Endpoint+path, bytes.NewReader(data))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range e.cfg.Headers {
		req.Header.Set(k, v)
	}
	resp, err := e.cfg.Client.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}

// Getenv is os.Getenv, named for FromSettings callers.
var Getenv = os.Getenv

// AllowedKeys returns every attribute key that may be exported, sorted.
func AllowedKeys() []string {
	out := make([]string, 0, len(allowedAttrs))
	for k := range allowedAttrs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
