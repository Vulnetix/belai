package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// Strands Decider-2B selected in settings answers the security guard for the
// built binary: with a decider server answering on loopback (its port in the
// run state, as a launch records it), the prompt's admission check goes to
// the decider and the chat provider sees no classifier turn. Nothing else is
// configured: no key, no profile.
func TestStrandsDeciderAnswersTheGuard(t *testing.T) {
	var decisions atomic.Int32
	decider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_, _ = w.Write([]byte(`{"status":"ok","model":"strands-decider-2b","checkpoint":"StrandsAgents/strands-decider-2B-hobson-v19","device":"cpu"}`))
			return
		}
		decisions.Add(1)
		var body struct {
			Questions map[string]struct {
				Type     string          `json:"type"`
				Criteria json.RawMessage `json:"criteria"`
			} `json:"questions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		answers := map[string]any{}
		for id, q := range body.Questions {
			if q.Type == "choice" {
				var crit map[string]any
				if json.Unmarshal(q.Criteria, &crit) != nil {
					http.Error(w, "criteria must be an object", http.StatusUnprocessableEntity)
					return
				}
				probs := map[string]float64{}
				for k := range crit {
					probs[k] = 1 / float64(len(crit))
				}
				answers[id] = map[string]any{"type": "choice", "probabilities": probs}
				continue
			}
			answers[id] = map[string]any{"type": "noul", "noul": 0.01}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "strands-decider-2b", "answers": answers})
	}))
	defer decider.Close()
	port := decider.URL[strings.LastIndexByte(decider.URL, ':')+1:]

	srv, mp := newMockServer(t)
	defer srv.Close()

	home := filepath.Join(t.TempDir(), "belai-home")
	if err := os.MkdirAll(filepath.Join(home, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "run", "strands-decider.json"), []byte(`{"port":`+port+`,"model":"decider-2b"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	settings := `{"classifier":{"kind":"systemone","provider":"strands-decider","model":"decider-2b"}}`
	if err := os.WriteFile(filepath.Join(home, "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BELAI_HOME", home)

	out, errOut, code := runBelai(t, srv.URL, "-tools=false", "-provider", "openai", "-model", "test", "-prompt", "what model is this")
	if code != 0 {
		t.Fatalf("exit = %d\nstderr: %s", code, errOut)
	}
	if !strings.Contains(out, "mock reply") {
		t.Fatalf("stdout = %q", out)
	}
	if decisions.Load() == 0 {
		t.Fatal("the decider was never asked")
	}
	mp.mu.Lock()
	defer mp.mu.Unlock()
	if len(mp.securityUser) != 0 {
		t.Fatalf("the chat provider answered the guard: %v", mp.securityUser)
	}
}
