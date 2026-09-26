package jev

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/rolemanager"
)

func TestDetectIntentScoresAllIntents(t *testing.T) {
	resp := decisionsResponse{
		Model: DefaultModel,
		Answers: map[string]noulAnswer{
			"agent":  {Type: "noul", Noul: 0.1},
			"plan":   {Type: "noul", Noul: 0.2},
			"goal":   {Type: "noul", Noul: 0.3},
			"debug":  {Type: "noul", Noul: 0.15},
			"fanout": {Type: "noul", Noul: 0.25},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/api/alpha/decisions") {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := New(func() (string, error) { return "token", nil })
	c.SetEndpoint(srv.URL)
	scores, model, err := c.DetectIntent(context.Background(), rolemanager.DetectInput{Prompt: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	if model != DefaultModel {
		t.Errorf("model = %q, want %q", model, DefaultModel)
	}
	if len(scores) != 5 {
		t.Fatalf("scores = %d, want 5", len(scores))
	}
	if scores[rolemanager.IntentGoal] != 0.3 {
		t.Errorf("goal score = %v, want 0.3", scores[rolemanager.IntentGoal])
	}
}

func TestDetectIntentOffersHandoffOnlyWithPlan(t *testing.T) {
	var lastReq testDetectRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &lastReq)
		w.Header().Set("Content-Type", "application/json")
		answers := map[string]noulAnswer{}
		for k := range lastReq.Questions {
			answers[k] = noulAnswer{Type: "noul", Noul: 0.1}
		}
		_ = json.NewEncoder(w).Encode(decisionsResponse{Model: DefaultModel, Answers: answers})
	}))
	defer srv.Close()

	c := New(func() (string, error) { return "token", nil })
	c.SetEndpoint(srv.URL)

	_, _, err := c.DetectIntent(context.Background(), rolemanager.DetectInput{Prompt: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := lastReq.Questions["handoff"]; ok {
		t.Fatal("handoff offered without plan attachment")
	}

	lastReq = testDetectRequest{}
	_, _, err = c.DetectIntent(context.Background(), rolemanager.DetectInput{
		Prompt:         "do it",
		PlanAttachment: &rolemanager.HandoffFacts{Tasks: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := lastReq.Questions["handoff"]; !ok {
		t.Fatal("handoff not offered with plan attachment")
	}
}

func TestDetectIntentPayloadHasNoAttachmentBytes(t *testing.T) {
	var lastReq testDetectRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &lastReq)
		w.Header().Set("Content-Type", "application/json")
		answers := map[string]noulAnswer{}
		for k := range lastReq.Questions {
			answers[k] = noulAnswer{Type: "noul", Noul: 0.1}
		}
		_ = json.NewEncoder(w).Encode(decisionsResponse{Model: DefaultModel, Answers: answers})
	}))
	defer srv.Close()

	c := New(func() (string, error) { return "token", nil })
	c.SetEndpoint(srv.URL)
	_, _, err := c.DetectIntent(context.Background(), rolemanager.DetectInput{
		Prompt:         "implement @plan.md",
		PlanAttachment: &rolemanager.HandoffFacts{Label: "plan.md", Tasks: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	stateStr := string(lastReq.State)
	if strings.Contains(stateStr, "implement @plan.md content") {
		t.Error("state contains attachment bytes")
	}
	if !strings.Contains(stateStr, "plan_file") {
		t.Error("state missing plan_file attachment kind")
	}
}

func TestDetectIntentMalformedAnswerErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(decisionsResponse{
			Model:   DefaultModel,
			Answers: map[string]noulAnswer{"agent": {Type: "noul", Noul: 1.5}},
		})
	}))
	defer srv.Close()

	c := New(func() (string, error) { return "token", nil })
	c.SetEndpoint(srv.URL)
	_, _, err := c.DetectIntent(context.Background(), rolemanager.DetectInput{Prompt: "do it"})
	if err == nil {
		t.Fatal("expected error for out-of-range noul")
	}
}

func TestDetectIntentTimeoutFallsBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(func() (string, error) { return "token", nil })
	c.SetEndpoint(srv.URL)
	c.client.Timeout = 10 * time.Millisecond
	_, _, err := c.DetectIntent(context.Background(), rolemanager.DetectInput{Prompt: "do it"})
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestDetectIntentStateHasNoToolsSkillsAgentBlock(t *testing.T) {
	var lastReq testDetectRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &lastReq)
		w.Header().Set("Content-Type", "application/json")
		answers := map[string]noulAnswer{}
		for k := range lastReq.Questions {
			answers[k] = noulAnswer{Type: "noul", Noul: 0.1}
		}
		_ = json.NewEncoder(w).Encode(decisionsResponse{Model: DefaultModel, Answers: answers})
	}))
	defer srv.Close()

	c := New(func() (string, error) { return "token", nil })
	c.SetEndpoint(srv.URL)
	_, _, err := c.DetectIntent(context.Background(), rolemanager.DetectInput{Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	bodyStr := string(lastReq.State)
	for _, forbidden := range []string{`"tools":`, `"skills":`, `"agent":`} {
		if strings.Contains(bodyStr, forbidden) {
			t.Errorf("request contains %s", forbidden)
		}
	}
}

func TestDetectIntentBuildRequestCarriesModeHint(t *testing.T) {
	var lastReq testDetectRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &lastReq)
		w.Header().Set("Content-Type", "application/json")
		answers := map[string]noulAnswer{}
		for k := range lastReq.Questions {
			answers[k] = noulAnswer{Type: "noul", Noul: 0.1}
		}
		_ = json.NewEncoder(w).Encode(decisionsResponse{Model: DefaultModel, Answers: answers})
	}))
	defer srv.Close()

	c := New(func() (string, error) { return "token", nil })
	c.SetEndpoint(srv.URL)
	_, _, err := c.DetectIntent(context.Background(), rolemanager.DetectInput{
		Prompt:   "hi",
		ModeHint: rolemanager.ModeHint{Mode: "plan", Sticky: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	stateStr := string(lastReq.State)
	if !strings.Contains(stateStr, "plan") {
		t.Errorf("state missing current mode: %s", stateStr)
	}
	if !strings.Contains(stateStr, "true") {
		t.Errorf("state missing sticky flag: %s", stateStr)
	}
}

// testDetectRequest mirrors the request body for test introspection.
type testDetectRequest struct {
	Model     string                  `json:"model"`
	Questions map[string]noulQuestion `json:"questions"`
	State     json.RawMessage         `json:"state"`
}
