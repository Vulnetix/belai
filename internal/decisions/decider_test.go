package decisions

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeciderCatalogue(t *testing.T) {
	m, ok := DeciderModelByID("decider-2b")
	if !ok || m.Label != "Strands Decider-2B" {
		t.Fatalf("DeciderModelByID = %+v, %v", m, ok)
	}
	if d, _ := DeciderModelByID(""); d.ID != m.ID {
		t.Errorf("the empty id names %q, want the first entry", d.ID)
	}
	if _, ok := DeciderModelByID("decider-4b"); ok {
		t.Error("decider-4b is a llama-server model, not a Strands Decider checkpoint")
	}
	if len(m.Revision) != 40 || len(m.BaseRevision) != 40 {
		t.Errorf("revisions must be pinned commits: %q %q", m.Revision, m.BaseRevision)
	}
	for _, f := range append(append([]DeciderFile{}, m.Checkpoint...), m.Base...) {
		if len(f.SHA256) != 64 || f.Size <= 0 || strings.Contains(f.Path, "..") {
			t.Errorf("file %+v is not pinned", f)
		}
	}
	if m.Size() < 4<<30 {
		t.Errorf("Size = %d, want the base model counted", m.Size())
	}
}

func TestIsDeciderID(t *testing.T) {
	for _, id := range []string{"strandsagents/strands-decider-2b", "StrandsAgents/strands-decider-2B-hobson-v19", "strands-decider-2b"} {
		if !IsDeciderID(id) {
			t.Errorf("IsDeciderID(%q) = false", id)
		}
	}
	for _, id := range []string{"typesafe/jev-1.13", "decider-4b", "openai/gpt-5", ""} {
		if IsDeciderID(id) {
			t.Errorf("IsDeciderID(%q) = true", id)
		}
	}
	if DeciderMaxOptions("decider-2b") != 24 || DeciderMaxOptions("strandsagents/strands-decider-2b") != 24 || DeciderMaxOptions("jev-latest") != 0 {
		t.Error("DeciderMaxOptions")
	}
}

func TestIsSystemOneKindReadsTheLegacyName(t *testing.T) {
	if !IsSystemOneKind("systemone") || !IsSystemOneKind("jev") || IsSystemOneKind("ollama") || IsSystemOneKind("") {
		t.Error("IsSystemOneKind")
	}
}

// The decider's server takes a choice's criteria only as an object; a bare
// list is a 422 there. CriteriaObject sends an object with null descriptions.
func TestSystemOneCriteriaObject(t *testing.T) {
	var body struct {
		Questions map[string]struct {
			Criteria json.RawMessage `json:"criteria"`
		} `json:"questions"`
	}
	s := systemOneServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"model":"strands-decider-2b","answers":{"mode":{"type":"choice","choice":"plan","probabilities":{"agent":0.2,"plan":0.7,"debug":0.1},"confidence":0.55}},"usage":{"input_tokens":40,"output_tokens":0},"latency_ms":12.5}`))
	})
	s.CriteriaObject = true
	res, err := s.Decide(context.Background(), Request{State: "x", Questions: map[string]Question{
		"mode": {Type: TypeChoice, Instructions: "Which mode?", Options: []string{"agent", "plan", "debug"}, Descriptions: map[string]string{"plan": "write a plan"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var crit map[string]*string
	if err := json.Unmarshal(body.Questions["mode"].Criteria, &crit); err != nil {
		t.Fatalf("criteria %s is not an object: %v", body.Questions["mode"].Criteria, err)
	}
	if len(crit) != 3 || crit["agent"] != nil || crit["plan"] == nil || *crit["plan"] != "write a plan" {
		t.Errorf("criteria = %s", body.Questions["mode"].Criteria)
	}
	if a := res.Answers["mode"]; a.Choice != "plan" || a.Probabilities["plan"] != 0.7 {
		t.Errorf("answer = %+v (confidence, usage and latency are ignored)", a)
	}
}

// Without CriteriaObject a choice with no descriptions keeps the list form
// TypeSafe and the other servers take.
func TestSystemOneCriteriaListByDefault(t *testing.T) {
	var raw map[string]any
	s := systemOneServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&raw)
		_, _ = w.Write([]byte(`{"answers":{"m":{"type":"choice","choice":"a","probabilities":{"a":0.6,"b":0.4}}}}`))
	})
	if _, err := s.Decide(context.Background(), Request{State: "x", Questions: map[string]Question{"m": {Type: TypeChoice, Instructions: "?", Options: []string{"a", "b"}}}}); err != nil {
		t.Fatal(err)
	}
	q := raw["questions"].(map[string]any)["m"].(map[string]any)
	if _, ok := q["criteria"].([]any); !ok {
		t.Errorf("criteria = %#v, want a list", q["criteria"])
	}
}

// A question with more options than the head reads is never sent: it fails
// as unavailable so the job's ordinary fallback answers.
func TestSystemOneMaxOptionsNeverSent(t *testing.T) {
	called := false
	s := systemOneServer(t, func(w http.ResponseWriter, r *http.Request) { called = true })
	s.MaxOptions = 3
	_, err := s.Decide(context.Background(), Request{State: "x", Questions: map[string]Question{"m": {Type: TypeChoice, Instructions: "?", Options: []string{"a", "b", "c", "d"}}}})
	if !IsUnavailable(err) || called {
		t.Fatalf("err = %v, called = %v; want unavailable without a request", err, called)
	}
}

// Resolve supplies the address per call, and a refused connection asks the
// supervisor to recover.
func TestSystemOneResolveAndRecover(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"answers":{"q":{"type":"noul","noul":0.1}}}`))
	}))
	t.Cleanup(srv.Close)
	url := srv.URL
	recovered := 0
	s := &SystemOne{Name: DeciderProvider, BaseURL: "http://127.0.0.1:1", Resolve: func() string { return url }, OnConnRefused: func() { recovered++ }}
	if _, err := s.Decide(context.Background(), Request{State: "x", Questions: map[string]Question{"q": Noul("p")}}); err != nil {
		t.Fatalf("resolved call: %v", err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	url = "http://" + l.Addr().String()
	_ = l.Close()
	_, err = s.Decide(context.Background(), Request{State: "x", Questions: map[string]Question{"q": Noul("p")}})
	if !IsUnavailable(err) || recovered != 1 {
		t.Fatalf("err = %v, recovered = %d; want unavailable and one recover", err, recovered)
	}
}
