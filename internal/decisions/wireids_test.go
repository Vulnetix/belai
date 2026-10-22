package decisions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Keys a server would refuse ("s:a1", "0:PROMPT_INJECTION") go out as safe
// stand-ins and come back under the caller's keys; safe keys go out as they
// are, and a stand-in never collides with one.
func TestQuestionKeysAreSafeOnTheWire(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Questions map[string]any `json:"questions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		ans := map[string]any{}
		for k := range body.Questions {
			if !wireIDPattern.MatchString(k) {
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
			n := 0.1
			if k == "q0" {
				n = 0.9
			}
			ans[k] = map[string]any{"type": "noul", "noul": n}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"answers": ans})
	}))
	defer srv.Close()
	s := &SystemOne{Name: "t", Model: "m", BaseURL: srv.URL, Client: srv.Client()}
	res, err := s.Decide(context.Background(), Request{State: "x", Questions: map[string]Question{
		"s:a1": Noul("a"), "0:PROMPT_INJECTION": Noul("b"), "q0": Noul("c"), "plain": Noul("d"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"s:a1", "0:PROMPT_INJECTION", "q0", "plain"} {
		if _, ok := res.Answers[k]; !ok {
			t.Fatalf("no answer under %q: %v", k, res.Answers)
		}
	}
	if res.Answers["q0"].Noul != 0.9 {
		t.Fatal("a caller's own safe key q0 was renamed")
	}
}
