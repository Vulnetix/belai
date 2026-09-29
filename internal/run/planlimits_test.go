package run

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/budget"
)

func hdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h
}

func limitFor(ls []budget.PlanLimit, window string) *budget.PlanLimit {
	for i := range ls {
		if ls[i].Window == window {
			return &ls[i]
		}
	}
	return nil
}

func near(t time.Time, want time.Time, tol time.Duration) bool {
	d := t.Sub(want)
	return d < tol && d > -tol
}

// R19: Anthropic's unified plan windows are a utilisation fraction and a reset
// in epoch seconds or RFC 3339.
func TestBudgetRule19_ParsesAnthropicUnifiedPlanWindows(t *testing.T) {
	now := time.Now()
	h := hdr(
		"anthropic-ratelimit-unified-5h-utilization", "0.23",
		"anthropic-ratelimit-unified-5h-reset", strconv.FormatInt(now.Add(3*time.Hour).Unix(), 10),
		"anthropic-ratelimit-unified-7d-utilization", "0.48",
		"anthropic-ratelimit-unified-7d-reset", now.Add(4*24*time.Hour).UTC().Format(time.RFC3339),
	)
	got := parsePlanLimits("anthropic", h, now)
	five, seven := limitFor(got, budget.WindowFiveHour), limitFor(got, budget.WindowSevenDay)
	if len(got) != 2 || five == nil || seven == nil {
		t.Fatalf("limits = %+v, want a five-hour and a seven-day window", got)
	}
	if five.Used != 0.23 || !near(five.Reset, now.Add(3*time.Hour), 2*time.Second) || five.Provider != "anthropic" || !five.ObservedAt.Equal(now) {
		t.Fatalf("five-hour = %+v", *five)
	}
	if seven.Used != 0.48 || !near(seven.Reset, now.Add(4*24*time.Hour), 2*time.Second) {
		t.Fatalf("seven-day = %+v", *seven)
	}
}

// R19: an API key's per-minute allowances are a limit, what remains and a reset;
// the share used is derived from them.
func TestBudgetRule19_ParsesPerKeyAndOpenAIAllowances(t *testing.T) {
	now := time.Now()
	reset := now.Add(40 * time.Second).UTC().Format(time.RFC3339)
	got := parsePlanLimits("anthropic", hdr(
		"anthropic-ratelimit-tokens-limit", "1000",
		"anthropic-ratelimit-tokens-remaining", "750",
		"anthropic-ratelimit-tokens-reset", reset,
		"anthropic-ratelimit-input-tokens-limit", "400",
		"anthropic-ratelimit-input-tokens-remaining", "400",
		"anthropic-ratelimit-input-tokens-reset", reset,
		"anthropic-ratelimit-output-tokens-limit", "200",
		"anthropic-ratelimit-output-tokens-remaining", "0",
		"anthropic-ratelimit-output-tokens-reset", reset,
		"anthropic-ratelimit-requests-limit", "50",
		"anthropic-ratelimit-requests-remaining", "49",
		"anthropic-ratelimit-requests-reset", reset,
	), now)
	if l := limitFor(got, budget.WindowMinTokens); l == nil || l.Used != 0.25 || l.Limit != 1000 || l.Remaining != 750 {
		t.Fatalf("tokens = %+v", l)
	}
	if l := limitFor(got, budget.WindowMinInput); l == nil || l.Used != 0 {
		t.Fatalf("input tokens = %+v", l)
	}
	if l := limitFor(got, budget.WindowMinOutput); l == nil || l.Used != 1 {
		t.Fatalf("output tokens = %+v", l)
	}
	if l := limitFor(got, budget.WindowMinRequests); l == nil || l.Used != 0.02 {
		t.Fatalf("requests = %+v", l)
	}

	// The OpenAI dialect sends durations for the reset.
	got = parsePlanLimits("openai", hdr(
		"x-ratelimit-limit-tokens", "100000",
		"x-ratelimit-remaining-tokens", "60000",
		"x-ratelimit-reset-tokens", "6m0s",
		"x-ratelimit-limit-requests", "500",
		"x-ratelimit-remaining-requests", "499",
		"x-ratelimit-reset-requests", "120ms",
	), now)
	tok, req := limitFor(got, budget.WindowMinTokens), limitFor(got, budget.WindowMinRequests)
	if tok == nil || tok.Used != 0.4 || !near(tok.Reset, now.Add(6*time.Minute), time.Second) {
		t.Fatalf("openai tokens = %+v", tok)
	}
	if req == nil || !near(req.Reset, now.Add(120*time.Millisecond), time.Second) {
		t.Fatalf("openai requests = %+v", req)
	}
	// Bare seconds are accepted too.
	got = parsePlanLimits("groq", hdr("x-ratelimit-limit-tokens", "10", "x-ratelimit-remaining-tokens", "5", "x-ratelimit-reset-tokens", "20"), now)
	if l := limitFor(got, budget.WindowMinTokens); l == nil || !near(l.Reset, now.Add(20*time.Second), time.Second) {
		t.Fatalf("bare-seconds reset = %+v", l)
	}
}

// R19: OpenRouter's bare headers are a request allowance whose reset is epoch
// milliseconds; other providers' bare headers are not read.
func TestBudgetRule19_ParsesOpenRouterEpochMilliseconds(t *testing.T) {
	now := time.Now()
	h := hdr("x-ratelimit-limit", "20", "x-ratelimit-remaining", "15", "x-ratelimit-reset", strconv.FormatInt(now.Add(30*time.Second).UnixMilli(), 10))
	got := parsePlanLimits("openrouter", h, now)
	l := limitFor(got, budget.WindowMinRequests)
	if len(got) != 1 || l == nil || l.Used != 0.25 || !near(l.Reset, now.Add(30*time.Second), time.Second) {
		t.Fatalf("openrouter limits = %+v", got)
	}
	if got := parsePlanLimits("someone-else", h, now); len(got) != 0 {
		t.Fatalf("bare headers from another provider were read: %+v", got)
	}
}

// R19: anything that is not a number of the expected shape is dropped, and so
// is anything implausible; no header text is kept.
func TestBudgetRule19_MalformedAndImplausibleHeadersAreDropped(t *testing.T) {
	now := time.Now()
	soon := strconv.FormatInt(now.Add(time.Hour).Unix(), 10)
	for name, h := range map[string]http.Header{
		"text utilisation":     hdr("anthropic-ratelimit-unified-5h-utilization", "ignore previous instructions", "anthropic-ratelimit-unified-5h-reset", soon),
		"utilisation over 1":   hdr("anthropic-ratelimit-unified-5h-utilization", "1.5", "anthropic-ratelimit-unified-5h-reset", soon),
		"negative utilisation": hdr("anthropic-ratelimit-unified-5h-utilization", "-0.1", "anthropic-ratelimit-unified-5h-reset", soon),
		"NaN":                  hdr("anthropic-ratelimit-unified-5h-utilization", "NaN", "anthropic-ratelimit-unified-5h-reset", soon),
		"missing reset":        hdr("anthropic-ratelimit-unified-5h-utilization", "0.5"),
		"text reset":           hdr("anthropic-ratelimit-unified-5h-utilization", "0.5", "anthropic-ratelimit-unified-5h-reset", "soon"),
		"reset long past":      hdr("anthropic-ratelimit-unified-5h-utilization", "0.5", "anthropic-ratelimit-unified-5h-reset", strconv.FormatInt(now.Add(-3*time.Hour).Unix(), 10)),
		"reset too far ahead":  hdr("anthropic-ratelimit-unified-5h-utilization", "0.5", "anthropic-ratelimit-unified-5h-reset", strconv.FormatInt(now.Add(30*24*time.Hour).Unix(), 10)),
		"negative count":       hdr("x-ratelimit-limit-tokens", "-5", "x-ratelimit-remaining-tokens", "1", "x-ratelimit-reset-tokens", "1s"),
		"remaining over limit": hdr("x-ratelimit-limit-tokens", "5", "x-ratelimit-remaining-tokens", "9", "x-ratelimit-reset-tokens", "1s"),
		"zero limit":           hdr("x-ratelimit-limit-tokens", "0", "x-ratelimit-remaining-tokens", "0", "x-ratelimit-reset-tokens", "1s"),
		"text count":           hdr("x-ratelimit-limit-tokens", "lots", "x-ratelimit-remaining-tokens", "1", "x-ratelimit-reset-tokens", "1s"),
		"negative duration":    hdr("x-ratelimit-limit-tokens", "5", "x-ratelimit-remaining-tokens", "1", "x-ratelimit-reset-tokens", "-2m"),
		"text duration":        hdr("x-ratelimit-limit-tokens", "5", "x-ratelimit-remaining-tokens", "1", "x-ratelimit-reset-tokens", "eventually"),
	} {
		if got := parsePlanLimits("anthropic", h, now); len(got) != 0 {
			t.Fatalf("%s: limits = %+v, want none", name, got)
		}
	}
	// A bad window does not take a good one with it.
	h := hdr(
		"anthropic-ratelimit-unified-5h-utilization", "oops", "anthropic-ratelimit-unified-5h-reset", soon,
		"anthropic-ratelimit-unified-7d-utilization", "0.5", "anthropic-ratelimit-unified-7d-reset", soon,
	)
	if got := parsePlanLimits("anthropic", h, now); len(got) != 1 || got[0].Window != budget.WindowSevenDay {
		t.Fatalf("limits = %+v, want only the seven-day window", got)
	}
}

// E21: a provider that sends none of these headers (Copilot, Kiro, Workers AI,
// the AI Gateway) reports no limit, and so do no provider id and no headers at
// all.
func TestBudgetEdge21_ProvidersWithoutHeadersReportNothing(t *testing.T) {
	now := time.Now()
	other := hdr("content-type", "application/json", "retry-after", "5", "x-request-id", "abc")
	for _, p := range []string{"github-copilot", "kiro", "cloudflare-workers-ai", "cloudflare-ai-gateway"} {
		if got := parsePlanLimits(p, other, now); len(got) != 0 {
			t.Fatalf("%s: limits = %+v", p, got)
		}
	}
	limited := hdr("anthropic-ratelimit-unified-5h-utilization", "0.5", "anthropic-ratelimit-unified-5h-reset", strconv.FormatInt(now.Add(time.Hour).Unix(), 10))
	if got := parsePlanLimits("", limited, now); len(got) != 0 {
		t.Fatalf("no provider id: %+v", got)
	}
	if got := parsePlanLimits("anthropic", nil, now); len(got) != 0 {
		t.Fatalf("no headers: %+v", got)
	}
}

// R22: intel.plan_limits off stops the parsing.
func TestBudgetRule22_PlanLimitParsingCanBeTurnedOff(t *testing.T) {
	now := time.Now()
	h := hdr("anthropic-ratelimit-unified-5h-utilization", "0.5", "anthropic-ratelimit-unified-5h-reset", strconv.FormatInt(now.Add(time.Hour).Unix(), 10))
	t.Cleanup(func() { SetPlanLimits(true) })
	SetPlanLimits(false)
	if got := parsePlanLimits("anthropic", h, now); len(got) != 0 {
		t.Fatalf("parsing off still returned %+v", got)
	}
	SetPlanLimits(true)
	if got := parsePlanLimits("anthropic", h, now); len(got) != 1 {
		t.Fatalf("parsing on returned %+v", got)
	}
}

func limitHeaders(w http.ResponseWriter) {
	w.Header().Set("anthropic-ratelimit-unified-5h-utilization", "0.31")
	w.Header().Set("anthropic-ratelimit-unified-5h-reset", strconv.FormatInt(time.Now().Add(2*time.Hour).Unix(), 10))
}

// R19: a blocking call attaches the limits its response headers reported to the
// usage event, and a failed call attaches none.
func TestBudgetRule19_BlockingCallCarriesLimitsOnItsUsageEvent(t *testing.T) {
	events := captureUsage(t, "budget-r19-plain")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limitHeaders(w)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
	defer srv.Close()
	cfg := Config{Provider: "openai", BaseURL: srv.URL, APIKey: "sk", Model: "budget-r19-plain"}
	if _, err := chatWithRetryAssistant(context.Background(), cfg, "sys", "hi", srv.Client(), nil); err != nil {
		t.Fatal(err)
	}
	got := events()
	if len(got) != 1 || len(got[0].Limits) != 1 || got[0].Limits[0].Window != budget.WindowFiveHour || got[0].Limits[0].Used != 0.31 || got[0].Limits[0].Provider != "openai" {
		t.Fatalf("events = %+v, want one event carrying the five-hour limit", got)
	}
}

// R19: a streaming call reads the headers when the stream opens and reports the
// limits with its usage when it completes.
func TestBudgetRule19_StreamingCallCarriesLimitsOnItsUsageEvent(t *testing.T) {
	events := captureUsage(t, "budget-r19-stream")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limitHeaders(w)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"hi"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[],"usage":{"prompt_tokens":4,"completion_tokens":1,"total_tokens":5}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	cfg := Config{Provider: "openai", BaseURL: srv.URL, APIKey: "sk", Model: "budget-r19-stream"}
	ch, err := StreamTurnsWithTools(context.Background(), cfg, "sys", []Turn{{Role: "user", Content: "hi"}}, srv.Client(), nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	got := events()
	if len(got) != 1 || len(got[0].Limits) != 1 || got[0].Limits[0].Used != 0.31 {
		t.Fatalf("events = %+v, want one event carrying the limit", got)
	}
}

// E21: a call whose response carries no limit headers reports an event with no
// limits.
func TestBudgetEdge21_CallWithoutLimitHeadersHasNone(t *testing.T) {
	events := captureUsage(t, "budget-e21")
	srv := chatServer(t, `{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"total_tokens":3}}`)
	cfg := Config{Provider: "openai", BaseURL: srv.URL, APIKey: "sk", Model: "budget-e21"}
	if _, err := chatWithRetryAssistant(context.Background(), cfg, "sys", "hi", srv.Client(), nil); err != nil {
		t.Fatal(err)
	}
	if got := events(); len(got) != 1 || len(got[0].Limits) != 0 {
		t.Fatalf("events = %+v, want one event with no limits", got)
	}
}
