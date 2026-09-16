package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/todos"
	"github.com/vulnetix/signet/internal/tools"
)

// goalPassOpts configures the scripted goal-pass mock server.
type goalPassOpts struct {
	mode  string   // operating-mode classifier reply (default "GOAL")
	eval  []string // goal-evaluator sentinel sequence
	main  string   // main model behaviour: "tool" | "length" | "reply"
	reply string   // main model final reply when main == "reply"
}

// goalPassServer records the main-model system prompts (so tests can assert the
// seal is byte-identical across passes) and scripts the classifier responses.
func goalPassServer(t *testing.T, opts goalPassOpts) (*httptest.Server, *sync.Mutex, *map[string]bool) {
	t.Helper()
	if opts.mode == "" {
		opts.mode = "GOAL"
	}
	if opts.main == "" {
		opts.main = "tool"
	}
	var mu sync.Mutex
	evalIdx := 0
	mainSystems := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var system string
		for _, m := range req.Messages {
			if m.Role == "system" {
				system = m.Content
			}
		}

		switch {
		case strings.Contains(system, "security classifier"):
			writeChatJSON(w, "SAFE")
		case strings.Contains(system, "operating-mode classifier"):
			writeChatJSON(w, opts.mode)
		case strings.Contains(system, "goal-progress evaluator"):
			mu.Lock()
			i := evalIdx
			evalIdx++
			mu.Unlock()
			if i >= len(opts.eval) {
				writeChatJSON(w, "GOAL_PARTIAL")
				return
			}
			writeChatJSON(w, opts.eval[i])
		default:
			mu.Lock()
			mainSystems[system] = true
			mu.Unlock()
			switch opts.main {
			case "length":
				writeLengthRepairJSON(w, "Read", `{"path":"f.txt"}`)
			case "reply":
				writeChatJSON(w, opts.reply)
			default:
				writeToolCallJSON(w, "Read", `{"path":"f.txt"}`)
			}
		}
	}))
	return srv, &mu, &mainSystems
}

// writeLengthRepairJSON returns a tool call with finish_reason "length", which
// the loop treats as truncated and withholds — an unproductive iteration.
func writeLengthRepairJSON(w http.ResponseWriter, name, args string) {
	b, _ := json.Marshal(map[string]any{
		"id":     "x",
		"object": "chat.completion",
		"choices": []any{map[string]any{
			"index": 0,
			"message": map[string]any{
				"role":    "assistant",
				"content": "",
				"tool_calls": []any{map[string]any{
					"id":       "call_1",
					"type":     "function",
					"function": map[string]any{"name": name, "arguments": args},
				}},
			},
			"finish_reason": "length",
		}},
	})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

func newGoalPassSession(t *testing.T, srv *httptest.Server, allowPassLoop bool, maxIter int) *Session {
	t.Helper()
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o600)

	cfg := run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "test-key", Model: "test"}
	sess, err := NewSession(Options{
		Cfg:           cfg,
		Client:        srv.Client(),
		Registry:      tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024}),
		Posture:       posture.Defaults(),
		AllowExplore:  true,
		AllowPassLoop: allowPassLoop,
		MaxIterations: maxIter,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return sess
}

func TestGoalPassLoopPartialPartialComplete(t *testing.T) {
	srv, mu, systems := goalPassServer(t, goalPassOpts{eval: []string{"GOAL_PARTIAL", "GOAL_PARTIAL", "GOAL_COMPLETE"}})
	defer srv.Close()
	sess := newGoalPassSession(t, srv, true, 2)

	res, err := sess.Run(context.Background(), "ship the thing")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.GoalSentinel != "GOAL_COMPLETE" {
		t.Fatalf("GoalSentinel = %q, want GOAL_COMPLETE", res.GoalSentinel)
	}
	if res.Passes != 3 {
		t.Fatalf("Passes = %d, want 3", res.Passes)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*systems) != 1 {
		t.Fatalf("expected one byte-identical sealed system prompt, got %d variants", len(*systems))
	}
}

func TestGoalPassLoopCompleteDowngradedWithoutVerification(t *testing.T) {
	srv, _, _ := goalPassServer(t, goalPassOpts{eval: []string{"GOAL_COMPLETE", "GOAL_COMPLETE"}})
	defer srv.Close()
	sess := newGoalPassSession(t, srv, true, 2)

	res, err := sess.Run(context.Background(), "ship the thing")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.GoalSentinel != "GOAL_COMPLETE" {
		t.Fatalf("GoalSentinel = %q, want GOAL_COMPLETE", res.GoalSentinel)
	}
	if res.Passes != 2 {
		t.Fatalf("Passes = %d, want 2 (complete on pass 1 is downgraded and forces one verification pass)", res.Passes)
	}
}

func TestGoalPassLoopTwoMalformedEvaluationsTerminate(t *testing.T) {
	srv, _, _ := goalPassServer(t, goalPassOpts{eval: []string{"garbage", "also garbage"}})
	defer srv.Close()
	sess := newGoalPassSession(t, srv, true, 2)

	_, err := sess.Run(context.Background(), "ship the thing")
	if err == nil {
		t.Fatal("expected an error after two consecutive malformed evaluations")
	}
	if !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("error = %v, want consecutive-malformed termination", err)
	}
}

func TestGoalPassLoopZeroProductiveDoesNotLoop(t *testing.T) {
	// finish_reason "length" withholds every tool result, so each pass is
	// unproductive and must not buy another pass.
	srv, _, _ := goalPassServer(t, goalPassOpts{main: "length", eval: []string{"GOAL_PARTIAL"}})
	defer srv.Close()
	sess := newGoalPassSession(t, srv, true, 2)

	_, err := sess.Run(context.Background(), "ship the thing")
	if err == nil || !strings.Contains(err.Error(), "no tools") {
		t.Fatalf("expected zero-productive termination, got %v", err)
	}
}

func TestGoalPassLoopDisabledReturnsMaxIterations(t *testing.T) {
	srv, _, _ := goalPassServer(t, goalPassOpts{eval: []string{"GOAL_COMPLETE"}})
	defer srv.Close()
	sess := newGoalPassSession(t, srv, false, 3)

	_, err := sess.Run(context.Background(), "ship the thing")
	if err == nil || !strings.Contains(err.Error(), "max iterations (3) reached") {
		t.Fatalf("expected max iterations error with pass loop disabled, got %v", err)
	}
}

func TestGoalPassLoopCancellation(t *testing.T) {
	srv, _, _ := goalPassServer(t, goalPassOpts{eval: []string{"GOAL_PARTIAL"}})
	defer srv.Close()
	sess := newGoalPassSession(t, srv, true, 3)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := sess.run(ctx, nil, TurnInput{Prompt: "ship the thing"}, false, func(e Event) {
		if e.Kind == EventToolStartKind {
			cancel()
		}
	})
	if err == nil {
		t.Fatal("expected an error from cancellation")
	}
	if !errors.Is(err, ErrPassLoopCancelled) {
		t.Fatalf("cancellation error = %v, want ErrPassLoopCancelled", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatal("cancellation must not surface raw context.Canceled")
	}
}

func TestPassTextExcludesToolResults(t *testing.T) {
	// A repository file containing [DONE:n] must never advance the todo list:
	// passOutcome.text accumulates assistant text only, never tool results.
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "f.txt"), []byte("[DONE:1] [DONE:2]"), 0o600)

	srv, _, _ := goalPassServer(t, goalPassOpts{})
	defer srv.Close()

	cfg := run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "test-key", Model: "test"}
	sess, err := NewSession(Options{
		Cfg:           cfg,
		Client:        srv.Client(),
		Registry:      tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024}),
		Posture:       posture.Defaults(),
		MaxIterations: 1,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	pipe := rolemanager.NewPipeline(run.NewClassifier(cfg, srv.Client()))
	out, _, err := sess.pass(context.Background(), pipe, "", nil, false, func(Event) {})
	if err != nil {
		t.Fatalf("pass: %v", err)
	}
	if strings.Contains(out.text, "[DONE:") {
		t.Fatalf("tool-result markers leaked into assistant text: %q", out.text)
	}

	l := passLedger{goalText: "goal"}
	l.advanceTodos(out.text)
	if l.hasList {
		t.Fatalf("tool-result markers must not build a todo list: %+v", l.list)
	}
}

func TestAdvanceTodosBuildsAndAdvancesFromAssistantText(t *testing.T) {
	l := passLedger{goalText: "goal"}
	l.advanceTodos("Plan:\n1. alpha\n2. beta\n")
	if !l.hasList || len(l.list.Items) != 2 {
		t.Fatalf("expected a 2-step list, got %+v", l.list)
	}
	if l.list.Items[0].Status != todos.StatusActive {
		t.Fatalf("item 1 should be active, got %q", l.list.Items[0].Status)
	}

	// Assistant markers advance the list and set a transition flag.
	l.advanceTodos("[DONE:1]")
	if l.list.Items[0].Status != todos.StatusDone || l.list.Items[1].Status != todos.StatusActive {
		t.Fatalf("assistant marker did not advance: %+v", l.list.Items)
	}
	if !l.todoChanged {
		t.Fatal("expected todoChanged=true after an assistant marker transition")
	}
}

func TestGoalPassLoopHonoursMaxPassesCeiling(t *testing.T) {
	// GOAL_PARTIAL forever: only the configured ceiling can stop this loop.
	srv, _, _ := goalPassServer(t, goalPassOpts{eval: []string{"GOAL_PARTIAL"}})
	defer srv.Close()

	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o600)
	sess, err := NewSession(Options{
		Cfg:           run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "test-key", Model: "test"},
		Client:        srv.Client(),
		Registry:      tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024}),
		Posture:       posture.Defaults(),
		AllowExplore:  true,
		AllowPassLoop: true,
		MaxIterations: 2,
		Settings:      config.Settings{Resilience: &config.ResilienceSettings{MaxPasses: 2}},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	res, err := sess.Run(context.Background(), "ship the thing")
	if err == nil || !strings.Contains(err.Error(), "max passes (2) reached") {
		t.Fatalf("expected the configured pass ceiling to stop the loop, got %v", err)
	}
	if res.Passes != 2 {
		t.Fatalf("Passes = %d, want 2", res.Passes)
	}
}

func TestGoalPassLoopUnboundedByDefault(t *testing.T) {
	// Without a ceiling the stall detector is what stops a stuck loop, not a
	// pass count: four no-progress partials terminate it.
	srv, _, _ := goalPassServer(t, goalPassOpts{eval: []string{"GOAL_PARTIAL"}})
	defer srv.Close()
	sess := newGoalPassSession(t, srv, true, 2)

	res, err := sess.Run(context.Background(), "ship the thing")
	if err == nil || !strings.Contains(err.Error(), "without todo progress") {
		t.Fatalf("expected stall termination, got %v", err)
	}
	if res.Passes <= 2 {
		t.Fatalf("Passes = %d, want more than the default ceiling would allow", res.Passes)
	}
}
