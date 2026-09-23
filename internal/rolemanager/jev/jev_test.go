package jev

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	c := New(func() (string, error) { return "test-key", nil })
	c.endpoint = srv.URL + "/api/alpha/decisions"
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
	if !strings.Contains(*reqBody, `"model":"typesafe/jev-1.13"`) ||
		!strings.Contains(*reqBody, `"type":"noul"`) ||
		!strings.Contains(*reqBody, "rm -rf /") {
		t.Fatalf("request body = %s, want a noul Decisions request carrying the tool call", *reqBody)
	}
}

func TestClassifyTransportErrorPropagates(t *testing.T) {
	c := New(func() (string, error) { return "", errors.New("no key") })
	if _, err := c.Classify(context.Background(), BuildPayload("bash ls")); err == nil {
		t.Fatal("Classify must propagate a token resolver error")
	}
}

func TestClassifyHTTPErrorPropagates(t *testing.T) {
	c, _, _ := newStubGate(t, http.StatusUnauthorized, `{"error":{"message":"bad key"}}`)
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
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"model":"typesafe/jev-1.13","answers":{"mode_eval":{"type":"noul","noul":0.92},"goal_eval":{"type":"noul","noul":0.03}},"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)
	c.endpoint = srv.URL

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
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"model":"x","answers":{},"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)
	c.endpoint = srv.URL

	got, err := c.Route(context.Background(), "mode select", []Candidate{{Key: "mode_eval", Provider: "openai", Model: "gpt-5"}})
	if err != nil {
		t.Fatalf("Route must not error on a malformed answer: %v", err)
	}
	if got.Key != "" {
		t.Fatalf("Route.Key = %q, want inconclusive", got.Key)
	}
}
