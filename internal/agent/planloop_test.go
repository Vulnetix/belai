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
	"github.com/vulnetix/signet/internal/tools"
)

// planPassOpts configures the scripted plan-pass mock server.
type planPassOpts struct {
	mode  string   // operating-mode classifier reply (default "PLAN")
	eval  []string // plan-evaluator sentinel sequence
	main  string   // main model behaviour: "tool" | "length" | "reply"
	reply string   // main model final reply when main == "reply"
}

// planPassServer records the main-model system prompts and scripts the
// classifier responses. It is the plan-mode analogue of goalPassServer.
func planPassServer(t *testing.T, opts planPassOpts) (*httptest.Server, *sync.Mutex, *map[string]bool) {
	t.Helper()
	if opts.mode == "" {
		opts.mode = "PLAN"
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
		case strings.Contains(system, "plan-progress evaluator"):
			mu.Lock()
			i := evalIdx
			evalIdx++
			mu.Unlock()
			if i >= len(opts.eval) {
				writeChatJSON(w, "PLAN_PARTIAL")
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

func newPlanPassSession(t *testing.T, srv *httptest.Server, allowPassLoop bool, maxIter int) *Session {
	t.Helper()
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o600)

	cfg := run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "test-key", Model: "test"}
	sess, err := NewSession(Options{
		Cfg:           cfg,
		Client:        srv.Client(),
		Registry:      tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024}),
		Posture:       posture.Defaults(),
		AllowExplore:  false, // keep the plan loop mechanics isolated from fan-out
		AllowPassLoop: allowPassLoop,
		MaxIterations: maxIter,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return sess
}

func TestPlanPassLoopPartialPartialComplete(t *testing.T) {
	srv, mu, systems := planPassServer(t, planPassOpts{eval: []string{"PLAN_PARTIAL", "PLAN_PARTIAL", "PLAN_COMPLETE"}})
	defer srv.Close()
	sess := newPlanPassSession(t, srv, true, 2)

	res, err := sess.Run(context.Background(), "write me a plan")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.PlanSentinel != "PLAN_COMPLETE" {
		t.Fatalf("PlanSentinel = %q, want PLAN_COMPLETE", res.PlanSentinel)
	}
	if res.GoalSentinel != "" {
		t.Fatalf("plan mode must not set GoalSentinel, got %q", res.GoalSentinel)
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

func TestPlanPassLoopCompleteOnNaturalExit(t *testing.T) {
	reply := "Plan:\n1. inspect the parser\n2. refactor the parser\n"
	srv, _, _ := planPassServer(t, planPassOpts{main: "reply", reply: reply, eval: []string{"PLAN_COMPLETE"}})
	defer srv.Close()
	sess := newPlanPassSession(t, srv, true, 2)

	res, err := sess.Run(context.Background(), "write me a plan")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.PlanSentinel != "PLAN_COMPLETE" {
		t.Fatalf("PlanSentinel = %q, want PLAN_COMPLETE", res.PlanSentinel)
	}
	if res.Passes != 1 {
		t.Fatalf("Passes = %d, want 1 (natural exit accepted on the first pass)", res.Passes)
	}
	if res.Reply != reply {
		t.Fatalf("Reply = %q, want %q", res.Reply, reply)
	}
}

func TestPlanPassLoopTwoMalformedEvaluationsTerminate(t *testing.T) {
	srv, _, _ := planPassServer(t, planPassOpts{eval: []string{"garbage", "also garbage"}})
	defer srv.Close()
	sess := newPlanPassSession(t, srv, true, 2)

	_, err := sess.Run(context.Background(), "write me a plan")
	if err == nil {
		t.Fatal("expected an error after two consecutive malformed evaluations")
	}
	if !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("error = %v, want consecutive-malformed termination", err)
	}
}

func TestPlanPassLoopZeroProductiveDoesNotLoop(t *testing.T) {
	srv, _, _ := planPassServer(t, planPassOpts{main: "length", eval: []string{"PLAN_PARTIAL"}})
	defer srv.Close()
	sess := newPlanPassSession(t, srv, true, 2)

	_, err := sess.Run(context.Background(), "write me a plan")
	if err == nil || !strings.Contains(err.Error(), "no tools") {
		t.Fatalf("expected zero-productive termination, got %v", err)
	}
}

func TestPlanPassLoopHonoursMaxPassesCeilingGracefully(t *testing.T) {
	// PLAN_PARTIAL forever: the bounded ceiling stops the loop and returns the
	// plan so far — a turn boundary, never an error.
	srv, _, _ := planPassServer(t, planPassOpts{main: "reply", reply: "plan so far", eval: []string{"PLAN_PARTIAL"}})
	defer srv.Close()

	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o600)
	sess, err := NewSession(Options{
		Cfg:           run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "test-key", Model: "test"},
		Client:        srv.Client(),
		Registry:      tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024}),
		Posture:       posture.Defaults(),
		AllowExplore:  false,
		AllowPassLoop: true,
		MaxIterations: 2,
		Settings:      config.Settings{Resilience: &config.ResilienceSettings{MaxPasses: 2}},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	res, err := sess.Run(context.Background(), "write me a plan")
	if err != nil {
		t.Fatalf("plan pass loop ceiling must not be an error, got %v", err)
	}
	if res.Passes != 2 {
		t.Fatalf("Passes = %d, want 2", res.Passes)
	}
	if res.PlanSentinel != "PLAN_PARTIAL" {
		t.Fatalf("PlanSentinel = %q, want PLAN_PARTIAL at the ceiling", res.PlanSentinel)
	}
	if res.Reply != "plan so far" {
		t.Fatalf("Reply = %q, want the plan so far", res.Reply)
	}
}

func TestPlanPassLoopDisabledStillBounded(t *testing.T) {
	// With AllowPassLoop false (a subagent), plan mode keeps the bounded
	// single-pass path and never contacts the plan evaluator.
	srv, _, _ := planPassServer(t, planPassOpts{eval: []string{"PLAN_COMPLETE"}})
	defer srv.Close()
	sess := newPlanPassSession(t, srv, false, 3)
	sess.settings = config.Settings{Resilience: &config.ResilienceSettings{MaxPasses: 1}}

	_, err := sess.Run(context.Background(), "write me a plan")
	if err != nil {
		t.Fatalf("budget exhaustion with the plan loop disabled must not be an error, got %v", err)
	}
}

func TestPlanPassLoopCancellation(t *testing.T) {
	srv, _, _ := planPassServer(t, planPassOpts{eval: []string{"PLAN_PARTIAL"}})
	defer srv.Close()
	sess := newPlanPassSession(t, srv, true, 3)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := sess.run(ctx, nil, TurnInput{Prompt: "write me a plan"}, false, func(e Event) {
		if e.Kind == EventToolStartKind {
			cancel()
		}
	})
	if err == nil {
		t.Fatal("expected an error from cancellation")
	}
	if !errors.Is(err, ErrPlanLoopCancelled) {
		t.Fatalf("cancellation error = %v, want ErrPlanLoopCancelled", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatal("cancellation must not surface raw context.Canceled")
	}
}

// TestPlanPassLoopSendsContextNotGoalToEvaluator pins the plan-mode boundary
// contract: the evaluator sees the exploration context gathered for the
// session, never a goal definition.
func TestPlanPassLoopSendsContextNotGoalToEvaluator(t *testing.T) {
	var mu sync.Mutex
	var sawContext, sawGoalWording bool
	classifier := rolemanager.ClassifierFunc(func(_ context.Context, p rolemanager.ClassifierPayload) (string, error) {
		if strings.Contains(p.System, "plan-progress evaluator") {
			mu.Lock()
			defer mu.Unlock()
			if strings.Contains(p.User, "exploration finding marker") {
				sawContext = true
			}
			if strings.Contains(p.User, "Goal:\n") {
				sawGoalWording = true
			}
			return "PLAN_COMPLETE", nil
		}
		return "SAFE", nil
	})
	pipe := rolemanager.NewPipeline(classifier)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeChatJSON(w, "Plan:\n1. do the thing\n")
	}))
	defer srv.Close()

	sess := newPlanPassSession(t, srv, true, 2)

	res, err := sess.planPassLoop(context.Background(), pipe, "", nil, "exploration finding marker", false, func(Event) {})
	if err != nil {
		t.Fatalf("planPassLoop: %v", err)
	}
	if res.PlanSentinel != "PLAN_COMPLETE" {
		t.Fatalf("PlanSentinel = %q, want PLAN_COMPLETE", res.PlanSentinel)
	}
	mu.Lock()
	defer mu.Unlock()
	if !sawContext {
		t.Fatal("plan evaluator did not receive the exploration context")
	}
	if sawGoalWording {
		t.Fatal("plan evaluator received goal wording")
	}
}

// TestExploreContextDigestJoinsFindings pins the digest the plan evaluator is
// shown: only non-empty exploration findings, in task order.
func TestExploreContextDigestJoinsFindings(t *testing.T) {
	got := exploreContextDigest(nil)
	if got != "" {
		t.Fatalf("empty findings = %q, want empty", got)
	}
	got = exploreContextDigest([]run.Turn{
		{Role: "user", Content: "  \n"},
		{Role: "user", Content: "finding one"},
		{Role: "user", Content: "finding two"},
	})
	if got != "finding one\n\nfinding two" {
		t.Fatalf("digest = %q", got)
	}
}

func TestPlanLedgerAdvancePlanBuildsAndAdvances(t *testing.T) {
	l := planLedger{context: "ctx"}
	l.advancePlan("Plan:\n1. alpha\n2. beta\n")
	if !l.hasList || len(l.list.Items) != 2 {
		t.Fatalf("expected a 2-step plan list, got %+v", l.list)
	}
	if l.list.Items[0].Status != "active" {
		t.Fatalf("item 1 should be active, got %q", l.list.Items[0].Status)
	}

	l.advancePlan("[DONE:1]")
	if l.list.Items[0].Status != "done" || l.list.Items[1].Status != "active" {
		t.Fatalf("assistant marker did not advance: %+v", l.list.Items)
	}
	if !l.todoChanged {
		t.Fatal("expected todoChanged=true after an assistant marker transition")
	}
}
