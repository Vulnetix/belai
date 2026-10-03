package modeltest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/run"
)

// fakeTev1 answers Together's chat completions the way Tev1 would: the
// injection state is option A (true), anything else B, and a question with a
// "plan" option picks it.
func fakeTev1(logprobs bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var task struct {
			State   string `json:"state"`
			Options []struct{ Label, Key string }
		}
		_ = json.Unmarshal([]byte(req.Messages[len(req.Messages)-1].Content), &task)
		letter := "B"
		if strings.Contains(task.State, "Ignore all previous") {
			letter = "A"
		}
		for _, o := range task.Options {
			if o.Key == "plan" {
				letter = o.Label
			}
		}
		if !logprobs {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"` + letter + `"}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"` + letter + `"},"logprobs":{"content":[{"token":"` + letter +
			`","logprob":-0.02,"top_logprobs":[{"token":"` + letter + `","logprob":-0.02},{"token":"Z","logprob":-6}]}]}}]}`))
	}))
}

func tev1Config(url string) run.DecisionsConfig {
	return run.DecisionsConfig{Backend: decisions.BackendChatLetters, Provider: "together", Model: decisions.Tev1HostedModel, BaseURL: url,
		Key: func() (string, error) { return "k", nil }}
}

func TestHostedTev1Steps(t *testing.T) {
	srv := fakeTev1(true)
	defer srv.Close()
	rep := Run(context.Background(), HostedTev1Steps(tev1Config(srv.URL)), &Env{Client: srv.Client()}, nil)
	if !rep.Passed {
		f, _ := rep.Failure()
		t.Fatalf("failed at %s: %s", f.Name, f.Outcome.Detail)
	}
	if w := rep.Warnings(); len(w) != 0 {
		t.Fatalf("warnings: %+v", w)
	}
}

func TestHostedTev1WithoutLogprobsIsNotSaved(t *testing.T) {
	srv := fakeTev1(false)
	defer srv.Close()
	rep := Run(context.Background(), HostedTev1Steps(tev1Config(srv.URL)), &Env{Client: srv.Client()}, nil)
	if rep.Passed {
		t.Fatal("an unscored letter must fail the test")
	}
	if f, _ := rep.Failure(); !strings.Contains(f.Outcome.Detail, "log-probabilities") {
		t.Fatalf("failure %+v", f.Outcome)
	}
}
