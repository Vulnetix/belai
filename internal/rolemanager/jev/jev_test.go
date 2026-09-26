package jev

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/rolemanager"
)

func TestIsDecisionsModel(t *testing.T) {
	cases := []struct {
		provider, model string
		want            bool
	}{
		{"openrouter", "typesafe/jev-1.13", true},
		{"openrouter", "typesafe/jev-1.13-20260917", true},
		{"openrouter", "openai/gpt-5", false},
		{"openai", "typesafe/jev-1.13", false},
		{"", "typesafe/jev-1.13", false},
		{"openrouter", "", false},
	}
	for _, tc := range cases {
		if got := IsDecisionsModel(tc.provider, tc.model); got != tc.want {
			t.Errorf("IsDecisionsModel(%q, %q) = %v, want %v", tc.provider, tc.model, got, tc.want)
		}
	}
}

func TestThreshold(t *testing.T) {
	cases := []struct {
		prob float64
		want Sentinel
	}{
		{0.99, Deny},
		{0.90, Deny},
		{0.50, Inconclusive},
		{0.10, Allow},
		{0.05, Allow},
	}
	for _, tc := range cases {
		if got := Threshold(tc.prob); got != tc.want {
			t.Errorf("Threshold(%v) = %q, want %q", tc.prob, got, tc.want)
		}
	}
}

func TestParseAnswer(t *testing.T) {
	cases := []struct {
		name string
		body string
		want Sentinel
	}{
		{"deny", `{"model":"typesafe/jev-1.13-20260917","answers":{"unsafe":{"type":"noul","noul":0.99}},"usage":{"input_tokens":1,"output_tokens":1}}`, Deny},
		{"allow", `{"model":"typesafe/jev-1.13-20260917","answers":{"unsafe":{"type":"noul","noul":0.05}},"usage":{"input_tokens":1,"output_tokens":1}}`, Allow},
		{"inconclusive", `{"model":"typesafe/jev-1.13-20260917","answers":{"unsafe":{"type":"noul","noul":0.5}},"usage":{"input_tokens":1,"output_tokens":1}}`, Inconclusive},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseAnswer([]byte(tc.body))
			if err != nil {
				t.Fatalf("ParseAnswer: %v", err)
			}
			if got != tc.want {
				t.Fatalf("ParseAnswer = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseAnswerMalformed(t *testing.T) {
	bad := []string{
		``,
		`not json`,
		`{"model":"x","answers":{},"usage":{"input_tokens":1,"output_tokens":1}}`,
		`{"model":"x","answers":{"unsafe":{"type":"choice","choice":"a"}},"usage":{"input_tokens":1,"output_tokens":1}}`,
		`{"model":"x","answers":{"unsafe":{"type":"noul","noul":1.5}},"usage":{"input_tokens":1,"output_tokens":1}}`,
	}
	for _, in := range bad {
		if _, err := ParseAnswer([]byte(in)); err == nil {
			t.Fatalf("ParseAnswer(%q) expected error", in)
		}
	}
}

func TestBuildPayloadIsToolless(t *testing.T) {
	p := BuildPayload(`{"name":"read","args":{"path":"a.txt"}}`)
	if p.Tools != nil || p.Skills != nil || p.Agent != "" {
		t.Fatalf("BuildPayload must be tool-less, skill-less, agent-less: %+v", p)
	}
	if p.User == "" {
		t.Fatal("BuildPayload must carry the tool call in User")
	}
}

// newStubGate returns a Client pointed at a Decisions API stub and the request
// it captured. body is the response body the stub returns.
func newStubGate(t *testing.T, status int, body string) (*Client, *string, *string) {
	t.Helper()
	var auth, reqBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/alpha/decisions" {
			t.Errorf("path = %q, want /api/alpha/decisions", r.URL.Path)
		}
		auth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		reqBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	c := New(func() (string, error) { return "test-key", nil })
	c.SetEndpoint(srv.URL)
	return c, &auth, &reqBody
}

func TestClassifySendsDecisionsRequestAndReturnsDeny(t *testing.T) {
	c, auth, reqBody := newStubGate(t, http.StatusOK,
		`{"model":"typesafe/jev-1.13-20260917","answers":{"unsafe":{"type":"noul","noul":0.99}},"usage":{"input_tokens":1,"output_tokens":1}}`)

	got, err := c.Classify(context.Background(), BuildPayload(`{"name":"bash","args":{"command":"rm -rf /"}}`))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != string(Deny) {
		t.Fatalf("Classify = %q, want %q", got, Deny)
	}
	if *auth != "Bearer test-key" {
		t.Fatalf("Authorization = %q, want Bearer test-key", *auth)
	}
	// The SDK serializes the request: model, a noul question map, and the tool
	// call carried as the state map value under "tool_call".
	for _, want := range []string{
		`"model":"typesafe/jev-1.13"`,
		`"type":"noul"`,
		`"tool_call"`,
		"rm -rf /",
	} {
		if !strings.Contains(*reqBody, want) {
			t.Fatalf("request body = %s, want it to contain %q", *reqBody, want)
		}
	}
}

func TestClassifyTransportErrorPropagates(t *testing.T) {
	c := New(func() (string, error) { return "", errors.New("no key") })
	if _, err := c.Classify(context.Background(), BuildPayload("bash ls")); err == nil {
		t.Fatal("Classify must propagate a token resolver error")
	}
}

func TestClassifyHTTPErrorPropagates(t *testing.T) {
	c, _, _ := newStubGate(t, http.StatusUnauthorized, `{"error":{"code":401,"message":"bad key"}}`)
	if _, err := c.Classify(context.Background(), BuildPayload("bash ls")); err == nil {
		t.Fatal("Classify must propagate a non-2xx Decisions response")
	}
}

func TestClassifyMalformedIsInconclusive(t *testing.T) {
	c, _, _ := newStubGate(t, http.StatusOK, `{"model":"x","answers":{},"usage":{"input_tokens":1,"output_tokens":1}}`)
	got, err := c.Classify(context.Background(), BuildPayload("bash ls"))
	if err != nil {
		t.Fatalf("Classify must not error on a malformed answer: %v", err)
	}
	if got != string(Inconclusive) {
		t.Fatalf("Classify = %q, want %q", got, Inconclusive)
	}
}

func TestSelectRoute(t *testing.T) {
	cases := []struct {
		name   string
		scores map[string]float64
		want   string
	}{
		{"clear winner", map[string]float64{"a": 0.9, "b": 0.1}, "a"},
		{"tie at top", map[string]float64{"a": 0.9, "b": 0.9}, ""},
		{"all below threshold", map[string]float64{"a": 0.4, "b": 0.1}, ""},
		{"single at threshold", map[string]float64{"a": 0.5}, ""},
		{"empty", map[string]float64{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SelectRoute(tc.scores); got != tc.want {
				t.Fatalf("SelectRoute(%v) = %q, want %q", tc.scores, got, tc.want)
			}
		})
	}
}

func TestParseRouteAnswer(t *testing.T) {
	candidates := []Candidate{{Key: "a"}, {Key: "b"}}
	scores, err := ParseRouteAnswer(
		[]byte(`{"model":"typesafe/jev-1.13","answers":{"a":{"type":"noul","noul":0.9},"b":{"type":"noul","noul":0.1}},"usage":{"input_tokens":1,"output_tokens":1}}`),
		candidates)
	if err != nil {
		t.Fatalf("ParseRouteAnswer: %v", err)
	}
	if scores["a"] != 0.9 || scores["b"] != 0.1 {
		t.Fatalf("scores = %v", scores)
	}

	bad := []string{
		``,
		`{"model":"x","answers":{},"usage":{"input_tokens":1,"output_tokens":1}}`,
		`{"model":"x","answers":{"a":{"type":"noul","noul":1.5}},"usage":{"input_tokens":1,"output_tokens":1}}`,
	}
	for _, in := range bad {
		if _, err := ParseRouteAnswer([]byte(in), candidates); err == nil {
			t.Fatalf("ParseRouteAnswer(%q) expected error", in)
		}
	}
}

func TestRouteSelectsUniqueWinner(t *testing.T) {
	c := New(func() (string, error) { return "test-key", nil })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"model":"typesafe/jev-1.13","answers":{"mode_eval":{"type":"noul","noul":0.92},"goal_eval":{"type":"noul","noul":0.03}},"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)
	c.SetEndpoint(srv.URL)

	got, err := c.Route(context.Background(), "mode select", []Candidate{
		{Key: "mode_eval", Provider: "openrouter", Model: "typesafe/jev-1.13"},
		{Key: "goal_eval", Provider: "openai", Model: "gpt-5-mini"},
	})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if got.Key != "mode_eval" {
		t.Fatalf("Route.Key = %q, want mode_eval (scores %v)", got.Key, got.Scores)
	}
	if got.Scores["mode_eval"] != 0.92 {
		t.Fatalf("scores = %v", got.Scores)
	}
}

func TestRouteMalformedIsInconclusive(t *testing.T) {
	c := New(func() (string, error) { return "test-key", nil })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"model":"x","answers":{},"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)
	c.SetEndpoint(srv.URL)

	got, err := c.Route(context.Background(), "mode select", []Candidate{{Key: "mode_eval", Provider: "openai", Model: "gpt-5"}})
	if err != nil {
		t.Fatalf("Route must not error on a malformed answer: %v", err)
	}
	if got.Key != "" {
		t.Fatalf("Route.Key = %q, want inconclusive", got.Key)
	}
}

// TestDecisions5xxIsOneAttempt pins the no-retry policy: a 5XX from the
// Decisions API returns at once as a *DecisionsError carrying the status and
// the server's message, instead of the SDK backing off for up to an hour.
func TestDecisions5xxIsOneAttempt(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body string
		status                  int
	}{
		{"typed json 500", "application/json", `{"error":{"code":500,"message":"decision backend failed"}}`, http.StatusInternalServerError},
		{"typed json 503", "application/json", `{"error":{"code":503,"message":"decision backend failed"}}`, http.StatusServiceUnavailable},
		{"html 502", "text/html", `<html>decision backend failed</html>`, http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			t.Cleanup(srv.Close)
			c := New(func() (string, error) { return "test-key", nil })
			c.SetEndpoint(srv.URL)

			_, err := c.Route(context.Background(), "mode_eval", []Candidate{{Key: "mode_eval", Provider: "openai", Model: "gpt-5"}})
			var de *DecisionsError
			if !errors.As(err, &de) {
				t.Fatalf("Route error = %v, want *DecisionsError", err)
			}
			if de.Status != tc.status {
				t.Fatalf("Status = %d, want %d", de.Status, tc.status)
			}
			if !strings.Contains(err.Error(), "decision backend failed") {
				t.Fatalf("error = %q, want the server message", err)
			}
			if calls != 1 {
				t.Fatalf("Decisions called %d times, want 1 (no retries)", calls)
			}
		})
	}
}

func TestDecisionsTokenErrorHasNoStatus(t *testing.T) {
	c := New(func() (string, error) { return "", errors.New("no key") })
	_, err := c.Route(context.Background(), "mode_eval", []Candidate{{Key: "mode_eval", Provider: "openai", Model: "gpt-5"}})
	var de *DecisionsError
	if !errors.As(err, &de) || de.Status != 0 {
		t.Fatalf("Route error = %v, want a *DecisionsError with Status 0", err)
	}
}

// --- Security classifier ---

// newStubSecurity returns a Security pointed at a Decisions API stub, the
// captured request, and a fallback that records every call.
func newStubSecurity(t *testing.T, status int, body string) (*Security, *string, *string, *int) {
	t.Helper()
	var auth, reqBody string
	fallbackCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/alpha/decisions" {
			t.Errorf("path = %q, want /api/alpha/decisions", r.URL.Path)
		}
		auth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		reqBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	fallback := rolemanager.ClassifierFunc(func(context.Context, rolemanager.ClassifierPayload) (string, error) {
		fallbackCalls++
		return string(rolemanager.SentinelSafe), nil
	})
	s := NewSecurity(func() (string, error) { return "test-key", nil }, fallback)
	s.SetEndpoint(srv.URL)
	return s, &auth, &reqBody, &fallbackCalls
}

func securityPayload() rolemanager.ClassifierPayload {
	return rolemanager.BuildClassifierPayload("untrusted content")
}

func TestSecuritySendsOneNoulQuestionPerCategory(t *testing.T) {
	s, auth, reqBody, fallbackCalls := newStubSecurity(t, http.StatusOK,
		`{"model":"typesafe/jev-1.13","answers":{"PROMPT_INJECTION":{"type":"noul","noul":0.02},"JAILBREAK":{"type":"noul","noul":0.02},"DATA_EXTRACTION":{"type":"noul","noul":0.02},"MODEL_EXTRACTION":{"type":"noul","noul":0.02}},"usage":{"input_tokens":1,"output_tokens":1}}`)

	got, err := s.Classify(context.Background(), securityPayload())
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != string(rolemanager.SentinelSafe) {
		t.Fatalf("Classify = %q, want SAFE", got)
	}
	if *fallbackCalls != 0 {
		t.Fatalf("fallback called %d times, want 0", *fallbackCalls)
	}
	if *auth != "Bearer test-key" {
		t.Fatalf("Authorization = %q, want Bearer test-key", *auth)
	}
	for _, want := range []string{
		`"model":"typesafe/jev-1.13"`,
		`"PROMPT_INJECTION"`,
		`"JAILBREAK"`,
		`"DATA_EXTRACTION"`,
		`"MODEL_EXTRACTION"`,
		`"content"`,
		"untrusted content",
	} {
		if !strings.Contains(*reqBody, want) {
			t.Fatalf("request body = %s, want it to contain %q", *reqBody, want)
		}
	}
}

func TestSecurityDenyPicksHighestCategory(t *testing.T) {
	s, _, _, fallbackCalls := newStubSecurity(t, http.StatusOK,
		`{"model":"typesafe/jev-1.13","answers":{"PROMPT_INJECTION":{"type":"noul","noul":0.95},"JAILBREAK":{"type":"noul","noul":0.99},"DATA_EXTRACTION":{"type":"noul","noul":0.02},"MODEL_EXTRACTION":{"type":"noul","noul":0.02}},"usage":{"input_tokens":1,"output_tokens":1}}`)

	got, err := s.Classify(context.Background(), securityPayload())
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != string(rolemanager.SentinelJailbreak) {
		t.Fatalf("Classify = %q, want JAILBREAK (highest)", got)
	}
	if *fallbackCalls != 0 {
		t.Fatalf("fallback called %d times, want 0", *fallbackCalls)
	}
}

func TestSecurityMidBandCallsFallback(t *testing.T) {
	s, _, _, fallbackCalls := newStubSecurity(t, http.StatusOK,
		`{"model":"typesafe/jev-1.13","answers":{"PROMPT_INJECTION":{"type":"noul","noul":0.5},"JAILBREAK":{"type":"noul","noul":0.02},"DATA_EXTRACTION":{"type":"noul","noul":0.02},"MODEL_EXTRACTION":{"type":"noul","noul":0.02}},"usage":{"input_tokens":1,"output_tokens":1}}`)

	got, err := s.Classify(context.Background(), securityPayload())
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != string(rolemanager.SentinelSafe) {
		t.Fatalf("fallback Classify = %q, want SAFE", got)
	}
	if *fallbackCalls != 1 {
		t.Fatalf("fallback called %d times, want 1", *fallbackCalls)
	}
}

func TestSecurityMalformedAnswerCallsFallback(t *testing.T) {
	s, _, _, fallbackCalls := newStubSecurity(t, http.StatusOK, `{"model":"x","answers":{},"usage":{"input_tokens":1,"output_tokens":1}}`)
	got, err := s.Classify(context.Background(), securityPayload())
	if err != nil {
		t.Fatalf("Classify must not error on a malformed answer: %v", err)
	}
	if got != string(rolemanager.SentinelSafe) {
		t.Fatalf("fallback Classify = %q, want SAFE", got)
	}
	if *fallbackCalls != 1 {
		t.Fatalf("fallback called %d times, want 1", *fallbackCalls)
	}
}

func TestSecurityHTTPErrorPropagates(t *testing.T) {
	s, _, _, fallbackCalls := newStubSecurity(t, http.StatusUnauthorized, `{"error":{"code":401,"message":"bad key"}}`)
	if _, err := s.Classify(context.Background(), securityPayload()); err == nil {
		t.Fatal("Classify must propagate a non-2xx Decisions response")
	}
	if *fallbackCalls != 0 {
		t.Fatalf("fallback called %d times, want 0", *fallbackCalls)
	}
}

func TestSecurityEmptyCategoriesCallsFallback(t *testing.T) {
	s, _, _, fallbackCalls := newStubSecurity(t, http.StatusOK, `{"model":"x","answers":{},"usage":{"input_tokens":1,"output_tokens":1}}`)
	p := rolemanager.ClassifierPayload{User: "x"}
	got, err := s.Classify(context.Background(), p)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != string(rolemanager.SentinelSafe) {
		t.Fatalf("fallback Classify = %q, want SAFE", got)
	}
	if *fallbackCalls != 1 {
		t.Fatalf("fallback called %d times, want 1", *fallbackCalls)
	}
}

// A Decisions endpoint that is down or slow hands the verdict to the agent
// model instead of withholding the tool result: the content is still
// classified. A refused request (401) still propagates, above.
func TestSecurityUnavailableEndpointCallsFallback(t *testing.T) {
	s, _, _, fallbackCalls := newStubSecurity(t, http.StatusBadGateway, `{"error":{"code":502,"message":"upstream"}}`)
	got, err := s.Classify(context.Background(), securityPayload())
	if err != nil {
		t.Fatalf("a 502 must fall back, not error: %v", err)
	}
	if got != string(rolemanager.SentinelSafe) || *fallbackCalls != 1 {
		t.Fatalf("got %q with %d fallback calls, want SAFE from one fallback call", got, *fallbackCalls)
	}
}

// Jev is a fast verdict model; one call is bounded at DecisionsTimeout (3s)
// and a timeout falls back rather than holding every tool result behind it.
func TestSecurityTimeoutCallsFallback(t *testing.T) {
	if DecisionsTimeout != 3*time.Second {
		t.Fatalf("DecisionsTimeout = %v, want 3s", DecisionsTimeout)
	}
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)
	calls := 0
	fallback := rolemanager.ClassifierFunc(func(context.Context, rolemanager.ClassifierPayload) (string, error) {
		calls++
		return string(rolemanager.SentinelSafe), nil
	})
	s := NewSecurity(func() (string, error) { return "k", nil }, fallback)
	s.client.Timeout = 50 * time.Millisecond
	s.SetEndpoint(srv.URL)
	got, err := s.Classify(context.Background(), securityPayload())
	if err != nil || got != string(rolemanager.SentinelSafe) || calls != 1 {
		t.Fatalf("timeout: got %q, err %v, fallback calls %d; want SAFE via one fallback", got, err, calls)
	}
}
