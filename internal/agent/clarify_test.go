package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/signet/internal/clarify"
	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/explore"
	"github.com/vulnetix/signet/internal/modes"
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

// roundedClarifyServer asks a different question each round ("Round 1?",
// "Round 2?", …) and records every clarifier request's user content.
func roundedClarifyServer(t *testing.T, users *[]string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		system, user := "", ""
		for _, m := range req.Messages {
			switch m.Role {
			case "system":
				system = m.Content
			case "user":
				user = m.Content
			}
		}
		reply := "SAFE"
		if strings.Contains(system, "clarification questionnaire") {
			mu.Lock()
			*users = append(*users, user)
			mu.Unlock()
			round := "1"
			if _, after, ok := strings.Cut(user, "Round: "); ok {
				round, _, _ = strings.Cut(after, "\n")
			}
			reply = `{"groups":[{"context":"Round ` + round + `?","options":[{"label":"A"},{"label":"B"}]}]}`
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
}

func TestClarifyRoundCapHonoured(t *testing.T) {
	var users []string
	srv := roundedClarifyServer(t, &users)
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

	answers, _ := s.clarifyRounds(ctx, pipe, rolemanager.ModeDecision{Explore: true}, "prompt", nil, emit)
	if calls != 2 {
		t.Fatalf("expected 2 questionnaire rounds, got %d", calls)
	}
	if len(answers) != 2 {
		t.Fatalf("expected 2 answer sets, got %d", len(answers))
	}
	// The second round must see the first round's question and answer.
	if len(users) != 2 || !strings.Contains(users[1], "Already answered:") || !strings.Contains(users[1], "Round 1?") {
		t.Fatalf("round 2 clarifier input lacks the earlier answer: %q", users)
	}
}

// TestClarifyNeverRepeatsAQuestion pins the fix for a clarifier that asked the
// same question every round: an already-asked question is dropped, and a
// questionnaire with nothing new ends the loop.
func TestClarifyNeverRepeatsAQuestion(t *testing.T) {
	srv := clarifyMockServer(t, singleQuestionJSON())
	defer srv.Close()

	s := newClarifySession(t, srv, 3)
	pipe := rolemanager.NewPipeline(run.NewClassifier(s.cfg, s.client))

	var asks int
	emit := func(e Event) {
		if e.Kind == EventClarifyAskKind && e.Reply != nil {
			asks++
			ans := answerFor(*e.Clarify)
			go func() { e.Reply <- ans }()
		}
	}
	answers, turns := s.clarifyRounds(context.Background(), pipe, rolemanager.ModeDecision{Explore: true}, "prompt", nil, emit)
	if asks != 1 {
		t.Fatalf("the same question was put to the user %d times, want 1", asks)
	}
	if len(answers) != 1 || !strings.Contains(answers[0], "chose: A") {
		t.Fatalf("answers = %q, want one set choosing A", answers)
	}
	if len(turns) != 0 {
		t.Fatalf("no survey ran, so no clarify fan-out is expected, got %d turns", len(turns))
	}
}

// TestClarifySkippedAllStops pins that declining every question ends the
// loop after one round: a re-ask would cost another clarify model call and
// another explore wave without carrying any new information into planning.
func TestClarifySkippedAllStops(t *testing.T) {
	var users []string
	srv := roundedClarifyServer(t, &users)
	defer srv.Close()

	s := newClarifySession(t, srv, 3)
	pipe := rolemanager.NewPipeline(run.NewClassifier(s.cfg, s.client))

	var asks int
	emit := func(e Event) {
		if e.Kind == EventClarifyAskKind && e.Reply != nil {
			asks++
			go func() { e.Reply <- clarify.Answers{Items: []clarify.Answer{{GroupIndex: 0, Skipped: true}}} }()
		}
	}
	answers, _ := s.clarifyRounds(context.Background(), pipe, rolemanager.ModeDecision{Explore: true}, "prompt", nil, emit)
	if asks != 1 {
		t.Fatalf("clarifier asked %d times after a full skip, want 1", asks)
	}
	if len(users) != 1 {
		t.Fatalf("clarifier calls = %d, want 1", len(users))
	}
	if len(answers) != 0 {
		t.Fatalf("answers = %q, want none for a full skip", answers)
	}
}

// TestClarifyWithoutFindingsUsesWorkspaceFacts pins the no-survey path: the
// clarifier's evidence is the harness-computed top-level listing, so its
// options can name real files.
func TestClarifyWithoutFindingsUsesWorkspaceFacts(t *testing.T) {
	var users []string
	srv := roundedClarifyServer(t, &users)
	defer srv.Close()

	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "README.md"), []byte("x"), 0o600)
	s := newClarifySession(t, srv, 1)
	s.workdir = root
	pipe := rolemanager.NewPipeline(run.NewClassifier(s.cfg, s.client))

	emit := func(e Event) {
		if e.Kind == EventClarifyAskKind && e.Reply != nil {
			ans := answerFor(*e.Clarify)
			go func() { e.Reply <- ans }()
		}
	}
	_, _ = s.clarifyRounds(context.Background(), pipe, rolemanager.ModeDecision{Explore: true}, "read a file", nil, emit)
	if len(users) != 1 || !strings.Contains(users[0], "README.md") {
		t.Fatalf("clarifier evidence lacks the workspace listing: %q", users)
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
	_, _ = s.clarifyRounds(context.Background(), pipe, rolemanager.ModeDecision{Explore: true}, "prompt", nil, emit)
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
	_, _ = s.clarifyRounds(context.Background(), pipe, rolemanager.ModeDecision{Explore: true}, "prompt", nil, emit)
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
	answers, turns := s.clarifyRounds(context.Background(), pipe, rolemanager.ModeDecision{Explore: true}, "prompt", nil, emit)
	if errors != 1 {
		t.Fatalf("expected exactly one refusal error, got %d", errors)
	}
	if len(answers)+len(turns) != 0 {
		t.Fatalf("refused answers should produce nothing, got %d answers, %d turns", len(answers), len(turns))
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
	answers, turns := s.clarifyRounds(ctx, pipe, rolemanager.ModeDecision{Explore: true}, "prompt", nil, emit)
	if len(answers)+len(turns) != 0 {
		t.Fatalf("cancelled ask should produce nothing, got %d answers, %d turns", len(answers), len(turns))
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

// TestPlanClarifyAnswersRideTheUserTurn pins where the answers land: on the
// user's prompt turn, as direction the planner must follow. They used to be
// appended to the exploration reports, which the model is told to treat as
// untrusted evidence, and the planner ignored the file the user picked.
func TestPlanClarifyAnswersRideTheUserTurn(t *testing.T) {
	var mu sync.Mutex
	var mainUsers []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		switch {
		case strings.Contains(system, "clarification questionnaire"):
			reply = `{"groups":[{"context":"Which file?","options":[{"label":"README.md"},{"label":"go.mod"}]}]}`
		case strings.Contains(system, "security classifier"):
		default:
			mu.Lock()
			for _, m := range req.Messages {
				if m.Role == "user" {
					mainUsers = append(mainUsers, m.Content)
				}
			}
			mu.Unlock()
			reply = "## Summary\nRead go.mod."
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

	s := newClarifySession(t, srv, 1)
	s.workdir = t.TempDir()
	emit := func(e Event) {
		if e.Kind == EventClarifyAskKind && e.Reply != nil {
			ans := clarify.Answers{Items: []clarify.Answer{{GroupIndex: 0, Chosen: []int{1}}}}
			go func() { e.Reply <- ans }()
		}
	}
	_, _ = s.run(context.Background(), nil, TurnInput{Prompt: "plan reading a file", ForceMode: modes.ModePlan}, false, emit)

	mu.Lock()
	defer mu.Unlock()
	for _, u := range mainUsers {
		if strings.HasPrefix(u, "plan reading a file") && strings.Contains(u, "chose: go.mod") && strings.Contains(u, "These answers are final") {
			return
		}
	}
	t.Fatalf("no planner request carried the answer on the prompt turn: %q", mainUsers)
}
