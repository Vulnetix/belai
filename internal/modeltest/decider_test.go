package modeltest

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/deciderserver"
	"github.com/vulnetix/belai/internal/decisions"
)

// A Strands Decider server already answering on loopback passes the ladder
// without a download or a launch, and the choice step sends object criteria.
func TestStrandsDeciderStepsOnARunningServer(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_, _ = w.Write([]byte(`{"status":"ok","model":"strands-decider-2b","checkpoint":"x/strands-decider-2b","device":"cuda"}`))
			return
		}
		var body struct {
			State     any `json:"state"`
			Questions map[string]struct {
				Type     string          `json:"type"`
				Criteria json.RawMessage `json:"criteria"`
			} `json:"questions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		q := body.Questions["q"]
		var ans any
		switch q.Type {
		case "choice":
			var crit map[string]any
			if json.Unmarshal(q.Criteria, &crit) != nil {
				http.Error(w, "criteria must be an object", http.StatusUnprocessableEntity)
				return
			}
			ans = map[string]any{"type": "choice", "choice": "plan", "probabilities": map[string]float64{"agent": 0.1, "plan": 0.8, "debug": 0.1}}
		default:
			p := 0.05
			if b, _ := json.Marshal(body.State); strings.Contains(string(b), "Ignore all previous") || strings.Contains(string(b), "read-only") || strings.Contains(string(b), "Write a plan") {
				p = 0.95
			}
			ans = map[string]any{"type": "noul", "noul": p}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"answers": map[string]any{"q": ans}})
	}))
	t.Cleanup(srv.Close)
	port, _ := strconv.Atoi(srv.URL[strings.LastIndexByte(srv.URL, ':')+1:])

	steps := StrandsDeciderSteps(DeciderTarget{Model: decisions.Decider2B, Opts: deciderserver.Options{Ports: []int{port}, Root: t.TempDir()}})
	asked := false
	rep := Run(context.Background(), steps, &Env{Client: srv.Client(), Confirm: func(context.Context, DownloadOffer) bool { asked = true; return false }}, nil)
	if !rep.Passed {
		f, _ := rep.Failure()
		t.Fatalf("failed at %s: %s", f.Name, f.Outcome.Detail)
	}
	if asked {
		t.Error("a running server must not offer a download")
	}
	if rep.Decider == nil || rep.Decider.Owned || rep.Decider.BaseURL != srv.URL {
		t.Errorf("decider handle = %+v", rep.Decider)
	}
	if rep.Steps[1].Outcome.Status != StatusSkip {
		t.Errorf("weights step = %+v, want skipped", rep.Steps[1].Outcome)
	}
}

// With no server and no command the first step fails with the install line.
func TestStrandsDeciderStepsNotInstalled(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	rep := Run(context.Background(), StrandsDeciderSteps(DeciderTarget{Model: decisions.Decider2B, Opts: deciderserver.Options{Ports: []int{port}, Root: t.TempDir()}}), &Env{}, nil)
	f, ok := rep.Failure()
	if rep.Passed || !ok || f.Name != "strands-decider" {
		t.Fatalf("report = %+v", rep)
	}
	text := f.Outcome.Detail
	for _, h := range f.Outcome.Hints {
		text += " " + h.Text
	}
	if !strings.Contains(text, "uv tool install strands-decider") {
		t.Errorf("no install line: %q", text)
	}
}
