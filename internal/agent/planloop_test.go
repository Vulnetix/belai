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

// planPassOpts configures the scripted plan-pass mock server.
type planPassOpts struct {
	mode  string   // operating-mode classifier reply (default "PLAN")
	eval  []string // plan-evaluator sentinel sequence
	main  string   // main model behaviour: "tool" | "length" | "reply"
	reply string   // main model final reply when main == "reply"
	// evalStatus, when non-zero, makes the plan evaluator return that HTTP
	// status instead of a sentinel, simulating a transport failure.
	evalStatus int
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
			if opts.evalStatus != 0 {
				w.WriteHeader(opts.evalStatus)
				return
			}
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

// newPlanPassSessionWithWorkdir is newPlanPassSession with an explicit
// Workdir so recordPlan writes plan files into a test temp dir rather than the
// package directory.
func newPlanPassSessionWithWorkdir(t *testing.T, srv *httptest.Server, allowPassLoop bool, maxIter int, workdir string) *Session {
	t.Helper()
	cfg := run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "test-key", Model: "test"}
	sess, err := NewSession(Options{
		Cfg:           cfg,
		Client:        srv.Client(),
		Registry:      tools.NewRegistry(&tools.Read{Root: workdir, MaxBytes: 1024}),
		Posture:       posture.Defaults(),
		AllowExplore:  false,
		AllowPassLoop: allowPassLoop,
		MaxIterations: maxIter,
		Workdir:       workdir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return sess
}

// assertPlanFileContains fails unless at least one recorded plan file under
// <workdir>/.vulnetix/plans contains want.
func assertPlanFileContains(t *testing.T, workdir, want string) {
	t.Helper()
	dir := filepath.Join(workdir, ".vulnetix", "plans")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read plans dir: %v", err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read plan file %s: %v", e.Name(), err)
		}
		if strings.Contains(string(data), want) {
			return
		}
	}
	t.Fatalf("no plan file under %s contains %q", dir, want)
}

func TestPlanPassLoopTransportEvalFailureRecordsPlanFile(t *testing.T) {
	reply := "Plan:\n1. inspect the downloader\n2. wire local inference\n"
	srv, _, _ := planPassServer(t, planPassOpts{main: "reply", reply: reply, evalStatus: http.StatusBadRequest})
	defer srv.Close()

	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o600)
	sess := newPlanPassSessionWithWorkdir(t, srv, true, 2, root)

	_, err := sess.Run(context.Background(), "write me a plan")
	if err == nil {
		t.Fatal("expected a terminal evaluator transport error")
	}
	assertPlanFileContains(t, root, "inspect the downloader")
}

func TestPlanPassLoopBrokenEvaluatorRecordsPlanFile(t *testing.T) {
	reply := "Plan:\n1. inspect the parser\n2. refactor the parser\n"
	srv, _, _ := planPassServer(t, planPassOpts{main: "reply", reply: reply, eval: []string{"garbage", "also garbage"}})
	defer srv.Close()

	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o600)
	sess := newPlanPassSessionWithWorkdir(t, srv, true, 2, root)

	_, err := sess.Run(context.Background(), "write me a plan")
	if err == nil {
		t.Fatal("expected a terminal broken-evaluator error")
	}
	assertPlanFileContains(t, root, "inspect the parser")
}

func TestPlanPassLoopZeroProductiveDoesNotLoop(t *testing.T) {
	srv, _, _ := planPassServer(t, planPassOpts{main: "length", eval: []string{"PLAN_PARTIAL"}})
	defer srv.Close()
	sess := newPlanPassSession(t, srv, true, 2)

	res, err := sess.Run(context.Background(), "write me a plan")
	if err != nil {
		t.Fatalf("expected zero-productive turn boundary, got error: %v", err)
	}
	if res.PlanSentinel != "PLAN_PARTIAL" {
		t.Fatalf("PlanSentinel = %q, want PLAN_PARTIAL", res.PlanSentinel)
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

// TestPlanPassLoopCeilingReturnsAccumulatedPlan pins the fix for the
// "returning the plan so far" bug: when the bounded ceiling is hit and the
// final pass produced only tool calls (no assistant text), the loop must still
// return the plan the model produced earlier — and write it to disk — rather
// than an empty reply that never materialises.
func TestPlanPassLoopCeilingReturnsAccumulatedPlan(t *testing.T) {
	var mu sync.Mutex
	mainCalls := 0
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
			writeChatJSON(w, "PLAN")
		case strings.Contains(system, "plan-progress evaluator"):
			writeChatJSON(w, "PLAN_PARTIAL")
		default:
			mu.Lock()
			n := mainCalls
			mainCalls++
			mu.Unlock()
			if n == 0 {
				writeChatJSON(w, "Plan:\n1. inspect the parser\n2. refactor the parser\n")
			} else {
				writeToolCallJSON(w, "Read", `{"path":"f.txt"}`)
			}
		}
	}))
	defer srv.Close()

	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o600)
	sess := newPlanPassSessionWithWorkdir(t, srv, true, 2, root)
	sess.settings = config.Settings{Resilience: &config.ResilienceSettings{MaxPasses: 2}}

	res, err := sess.Run(context.Background(), "write me a plan")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.PlanSentinel != "PLAN_PARTIAL" {
		t.Fatalf("PlanSentinel = %q, want PLAN_PARTIAL", res.PlanSentinel)
	}
	if !strings.Contains(res.Reply, "inspect the parser") {
		t.Fatalf("Reply = %q, want the accumulated plan", res.Reply)
	}
	assertPlanFileContains(t, root, "inspect the parser")
}

// TestPlanPassLoopCancellationRecordsPlanFile pins the plan-file contract on
// the cancellation path: a deliberate esc after planning work has begun still
// writes the plan gathered so far to disk before surfacing ErrPlanLoopCancelled.
func TestPlanPassLoopCancellationRecordsPlanFile(t *testing.T) {
	var mu sync.Mutex
	mainCalls := 0
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
			writeChatJSON(w, "PLAN")
		case strings.Contains(system, "plan-progress evaluator"):
			writeChatJSON(w, "PLAN_PARTIAL")
		default:
			mu.Lock()
			n := mainCalls
			mainCalls++
			mu.Unlock()
			if n == 0 {
				writeChatJSON(w, "Plan:\n1. inspect the parser\n2. refactor the parser\n")
			} else {
				writeToolCallJSON(w, "Read", `{"path":"f.txt"}`)
			}
		}
	}))
	defer srv.Close()

	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o600)
	sess := newPlanPassSessionWithWorkdir(t, srv, true, 3, root)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := sess.run(ctx, nil, TurnInput{Prompt: "write me a plan"}, false, func(e Event) {
		if e.Kind == EventToolStartKind {
			cancel()
		}
	})
	if !errors.Is(err, ErrPlanLoopCancelled) {
		t.Fatalf("cancellation error = %v, want ErrPlanLoopCancelled", err)
	}
	assertPlanFileContains(t, root, "inspect the parser")
}

// TestPlanLedgerPlanSoFarPreference pins the artifact-fallback ordering:
// latest plan-shaped text wins, then the rendered todo list, then any
// assistant prose.
func TestPlanLedgerPlanSoFarPreference(t *testing.T) {
	l := planLedger{}
	if got := l.planSoFar(); got != "" {
		t.Fatalf("empty ledger planSoFar = %q, want empty", got)
	}

	l.list = todos.New("", []string{"step a", "step b"})
	l.hasList = true
	if got := l.planSoFar(); !strings.Contains(got, "1. step a") || !strings.Contains(got, "2. step b") {
		t.Fatalf("todo fallback = %q", got)
	}

	l.bestPlan = "Plan:\n1. the real plan"
	if got := l.planSoFar(); got != "Plan:\n1. the real plan" {
		t.Fatalf("bestPlan must win, got %q", got)
	}

	prose := planLedger{lastText: "plain prose"}
	if got := prose.planSoFar(); got != "plain prose" {
		t.Fatalf("lastText fallback = %q", got)
	}
}

// TestPlanLedgerDirectiveEscalation pins the escalating finish-or-check-in
// wording and the steps-taken/remaining summary the model is shown as the
// bounded loop approaches its ceiling.
func TestPlanLedgerDirectiveEscalation(t *testing.T) {
	l := planLedger{maxPasses: 5, passes: 1}
	if got := l.planUrgency(); !strings.Contains(got, "finalised promptly") {
		t.Fatalf("early urgency = %q", got)
	}

	l.passes = 4
	if got := l.planUrgency(); !strings.Contains(got, "final planning pass") || !strings.Contains(got, "ExitPlanMode") {
		t.Fatalf("late urgency = %q", got)
	}

	l.passes = 1
	l.list = todos.New("", []string{"read the parser", "write the plan"})
	l.hasList = true
	l.list.Items[0].Status = todos.StatusDone
	l.list.Items[1].Status = todos.StatusActive
	got := l.planPartialDirective(true)
	for _, want := range []string{
		planBudgetNote,
		"The plan is partially complete",
		"Steps tracked: 1 done, 1 in progress, 0 remaining",
		"Fold the concrete tool calls you have made",
		"Current plan todo list",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("partial directive missing %q:\n%s", want, got)
		}
	}

	started := l.planStartDirective(true)
	if !strings.HasPrefix(started, planBudgetNote) {
		t.Fatalf("start directive must carry the budget note, got %q", started)
	}
}
