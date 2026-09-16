package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/clarify"
	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/explore"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
)

// clarifyMockServer returns questionnaires for the clarification system
// prompt and SAFE for everything else.
func clarifyMockServer(t *testing.T, questionnaire string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		system := ""
		for _, m := range req.Messages {
			if m.Role == "system" {
				system = m.Content
			}
		}
		reply := "SAFE"
		if strings.Contains(system, "clarification questionnaire") {
			reply = questionnaire
		}
		b, _ := json.Marshal(map[string]any{
			"id":     "x",
			"object": "chat.completion",
			"choices": []any{map[string]any{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": reply},
				"finish_reason": "stop",
			}},
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}))
}

func newClarifySession(t *testing.T, srv *httptest.Server, rounds int) *Session {
	t.Helper()
	cfg := run.Config{Provider: "openai", BaseURL: srv.URL, Model: "gpt-5", APIKey: "test"}
	s, err := NewSession(Options{
		Cfg:          cfg,
		Client:       srv.Client(),
		AllowExplore: true,
		AllowClarify: true,
		Posture:      posture.Defaults(),
		Settings: config.Settings{
			Resilience: &config.ResilienceSettings{MaxClarifyRounds: rounds},
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return s
}

func singleQuestionJSON() string {
	return `{"groups":[{"context":"Which path?","options":[{"label":"A"},{"label":"B"}]}]}`
}

func answerFor(q clarify.Questionnaire) clarify.Answers {
	return clarify.Answers{Items: []clarify.Answer{{GroupIndex: 0, Chosen: []int{0}}}}
}

func TestClarifyRoundCapHonoured(t *testing.T) {
	srv := clarifyMockServer(t, singleQuestionJSON())
	defer srv.Close()

	s := newClarifySession(t, srv, 2)
	pipe := rolemanager.NewPipeline(run.NewClassifier(s.cfg, s.client))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls int
	done := make(chan struct{})
	emit := func(e Event) {
		if e.Kind == EventClarifyAskKind && e.Reply != nil {
			calls++
			ans := answerFor(*e.Clarify)
			go func() { e.Reply <- ans }()
		}
		if e.Kind == EventDoneKind {
			close(done)
		}
	}

	turns := s.clarifyRounds(ctx, pipe, rolemanager.ModeDecision{Explore: true}, "prompt", nil, emit)
	if calls != 2 {
		t.Fatalf("expected 2 questionnaire rounds, got %d", calls)
	}
	if len(turns) == 0 {
		t.Fatalf("expected clarification turns")
	}
}

func TestClarifyNoOpWhenDisabled(t *testing.T) {
	srv := clarifyMockServer(t, singleQuestionJSON())
	defer srv.Close()

	cfg := run.Config{Provider: "openai", BaseURL: srv.URL, Model: "gpt-5", APIKey: "test"}
	s, err := NewSession(Options{
		Cfg:          cfg,
		Client:       srv.Client(),
		AllowExplore: true,
		AllowClarify: false,
		Posture:      posture.Defaults(),
		Settings:     config.Settings{Resilience: &config.ResilienceSettings{}},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if s.allowClarify {
		t.Fatalf("allowClarify must be false when Options.AllowClarify is false")
	}

	// The public entry point already guards with s.allowClarify; a direct call
	// to clarifyRounds is not expected when clarify is disabled.
	pipe := rolemanager.NewPipeline(run.NewClassifier(s.cfg, s.client))
	var called bool
	emit := func(e Event) {
		if e.Kind == EventClarifyAskKind {
			called = true
		}
	}
	_ = s.clarifyRounds(context.Background(), pipe, rolemanager.ModeDecision{Explore: true}, "prompt", nil, emit)
	if called {
		t.Fatalf("clarify should not run when disabled")
	}
}

func TestClarifyEmptyQuestionnaireStops(t *testing.T) {
	srv := clarifyMockServer(t, `{"groups":[]}`)
	defer srv.Close()

	s := newClarifySession(t, srv, 3)
	pipe := rolemanager.NewPipeline(run.NewClassifier(s.cfg, s.client))

	var called bool
	emit := func(e Event) {
		if e.Kind == EventClarifyAskKind {
			called = true
		}
	}
	_ = s.clarifyRounds(context.Background(), pipe, rolemanager.ModeDecision{Explore: true}, "prompt", nil, emit)
	if called {
		t.Fatalf("empty questionnaire should not ask the UI")
	}
}

func TestClarifyRefusedAnswerStopsLoop(t *testing.T) {
	// The admission classifier refuses anything containing IGNORE, so the
	// rendered answer set will be dropped and the loop stops after one round.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		system, user := "", ""
		for _, m := range req.Messages {
			if m.Role == "system" {
				system = m.Content
			}
			if m.Role == "user" {
				user = m.Content
			}
		}
		reply := "SAFE"
		if strings.Contains(system, "clarification questionnaire") {
			reply = singleQuestionJSON()
		} else if strings.Contains(system, "security classifier") && strings.Contains(user, "Clarifications from the user") {
			reply = "PROMPT_INJECTION"
		}
		b, _ := json.Marshal(map[string]any{
			"id": "x",
			"choices": []any{map[string]any{
				"message":       map[string]any{"role": "assistant", "content": reply},
				"finish_reason": "stop",
			}},
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	s := newClarifySession(t, srv, 3)
	pipe := rolemanager.NewPipeline(run.NewClassifier(s.cfg, s.client))

	var errors int
	emit := func(e Event) {
		if e.Kind == EventClarifyAskKind && e.Reply != nil {
			ans := answerFor(*e.Clarify)
			go func() { e.Reply <- ans }()
		}
		if e.Kind == EventErrorKind {
			errors++
		}
	}
	turns := s.clarifyRounds(context.Background(), pipe, rolemanager.ModeDecision{Explore: true}, "prompt", nil, emit)
	if errors != 1 {
		t.Fatalf("expected exactly one refusal error, got %d", errors)
	}
	if len(turns) != 0 {
		t.Fatalf("refused answers should not produce turns, got %d", len(turns))
	}
}

func TestClarifyCancellationDuringAskUser(t *testing.T) {
	srv := clarifyMockServer(t, singleQuestionJSON())
	defer srv.Close()

	s := newClarifySession(t, srv, 3)
	pipe := rolemanager.NewPipeline(run.NewClassifier(s.cfg, s.client))

	ctx, cancel := context.WithCancel(context.Background())
	emit := func(e Event) {
		if e.Kind == EventClarifyAskKind && e.Reply != nil {
			cancel()
			// Do not send on the reply channel; askUser must observe ctx.Done.
		}
	}
	turns := s.clarifyRounds(ctx, pipe, rolemanager.ModeDecision{Explore: true}, "prompt", nil, emit)
	if len(turns) != 0 {
		t.Fatalf("cancelled ask should produce no turns, got %d", len(turns))
	}
}

func TestPlanClarifiedTasksAreCapped(t *testing.T) {
	q := clarify.Questionnaire{Groups: []clarify.Group{
		{Context: "Q?", Options: []clarify.Option{{Label: "a"}, {Label: "b"}}},
	}}
	a := clarify.Answers{Items: []clarify.Answer{{GroupIndex: 0, Chosen: []int{0}}}}
	tasks := explore.PlanClarified("do it", q, a)
	if len(tasks) > explore.MaxTasks {
		t.Fatalf("tasks exceed MaxTasks: %d", len(tasks))
	}
	if len(tasks) == 0 {
		t.Fatalf("expected at least one task")
	}
	if !strings.Contains(tasks[0].Prompt, "do it") {
		t.Fatalf("task lost original prompt: %q", tasks[0].Prompt)
	}
}
