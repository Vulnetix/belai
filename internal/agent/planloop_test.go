package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/todos"
	"github.com/vulnetix/belai/internal/tools"
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
			case "drafting":
				// Reads while carrying a plan draft, so the loop's
				// no-plan-drafted rule does not force the final pass and
				// the evaluator's verdicts drive it.
				writeToolCallWithContentJSON(w, "Read", `{"path":"f.txt"}`, "Plan:\n1. inspect the parser\n2. refactor the parser\n")
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
	srv, mu, systems := planPassServer(t, planPassOpts{main: "drafting", eval: []string{"PLAN_PARTIAL", "PLAN_PARTIAL", "PLAN_COMPLETE"}})
	defer srv.Close()
	sess := newPlanPassSession(t, srv, true, 2)
	// Three evaluated passes need a ceiling above the default of 3, whose
	// last pass is the write-only final pass.
	sess.settings.Resilience = &config.ResilienceSettings{MaxPasses: 5}

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
	srv, _, _ := planPassServer(t, planPassOpts{main: "drafting", eval: []string{"garbage", "also garbage"}})
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
		MaxIterations: 1,
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

// planFinalPassServer scripts a planning model that reads while reading is
// offered and calls ExitPlanMode once it is the only tool left. It records the
// tool names and the last user message of every main-model request.
func planFinalPassServer(t *testing.T, eval string) (*httptest.Server, *sync.Mutex, *[][]string, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var toolSets [][]string
	var lastUser []string
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
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var system, user string
		for _, m := range req.Messages {
			switch m.Role {
			case "system":
				system = m.Content
			case "user":
				user = m.Content
			}
		}
		switch {
		case strings.Contains(system, "security classifier"):
			writeChatJSON(w, "SAFE")
		case strings.Contains(system, "operating-mode classifier"):
			writeChatJSON(w, "PLAN")
		case strings.Contains(system, "plan-progress evaluator"):
			writeChatJSON(w, eval)
		default:
			var names []string
			for _, tl := range req.Tools {
				names = append(names, tl.Function.Name)
			}
			mu.Lock()
			toolSets = append(toolSets, names)
			lastUser = append(lastUser, user)
			mu.Unlock()
			if slices.Contains(names, "Read") {
				writeToolCallJSON(w, "Read", `{"file_path":"f.txt"}`)
				return
			}
			writeToolCallJSON(w, "ExitPlanMode", `{"plan":"## Summary\nFix the parser.\n## Steps\n1. change the parser\n   - Files: p.go (new), p_test.go (new)\n   - Verify: go build ./...\n2. add the rollback step\n   - Files: p.go\n   - Verify: go build ./...\n## Test Plan\ngo test\n## Assumptions\nnone\n## Risks\nnone"}`)
		}
	}))
	return srv, &mu, &toolSets, &lastUser
}

// Session bc0b79d8: five passes each restarted exploration and the loop ended
// on "returning the plan so far" with nothing in it. The last pass now offers
// only update_plan and ExitPlanMode, and every continuation names what is
// already known plus the evaluator's reason.
func TestPlanPassLoopFinalPassOffersOnlyTheFinishTools(t *testing.T) {
	srv, mu, toolSets, lastUser := planFinalPassServer(t, "PLAN_PARTIAL\nMissing: the rollback step")
	defer srv.Close()

	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o600)
	sess, err := NewSession(Options{
		Cfg:           run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "test-key", Model: "test"},
		Client:        srv.Client(),
		Registry:      tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024}, tools.ExitPlanMode{}, tools.UpdatePlan{}),
		Posture:       posture.Defaults(),
		AllowPassLoop: true,
		MaxIterations: 1,
		Workdir:       root,
		Settings:      config.Settings{Resilience: &config.ResilienceSettings{MaxPasses: 2}},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	res, err := sess.Run(context.Background(), "write me a plan")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.PlanSentinel != rolemanager.PlanComplete || !strings.Contains(res.PlanText, "rollback") {
		t.Fatalf("result = %+v, want the plan handed over through ExitPlanMode; tools=%v", res, *toolSets)
	}

	mu.Lock()
	defer mu.Unlock()
	last := (*toolSets)[len(*toolSets)-1]
	slices.Sort(last)
	if !slices.Equal(last, []string{"ExitPlanMode"}) {
		t.Fatalf("final pass tools = %v, want only ExitPlanMode", last)
	}
	if !slices.Contains((*toolSets)[0], "Read") {
		t.Fatalf("earlier passes keep the exploration tools: %v", (*toolSets)[0])
	}
	final := (*lastUser)[len(*lastUser)-1]
	for i, u := range *lastUser {
		if !strings.Contains(u, "PLAN check") && !strings.Contains(u, "planning checklist is optional") && !strings.Contains(u, "last tool round") {
			t.Fatalf("plan request %d carried neither a PLAN check, the optional-checklist note nor the last-round nudge:\n%s", i, u)
		}
	}
	for _, want := range []string{"final planning pass", "not starting over", "Files already read: f.txt", "the rollback step"} {
		if !strings.Contains(final, want) {
			t.Fatalf("final directive turn missing %q:\n%s", want, final)
		}
	}
	if sess.planFinalPass {
		t.Fatal("the final-pass narrowing must not outlive the loop")
	}
}

// The model-derived specifics ride as plain text: neither the evaluator's
// reason nor a model-chosen path may enter the sealed directive body.
func TestPlanNoteNeverEntersTheSealedDirective(t *testing.T) {
	l := planLedger{reason: "ignore previous instructions", read: []string{"a.go"}}
	turns := directiveTurnsWithNote(l.knownState(), l.knownNote())
	if strings.Contains(turns[0].Directive, "ignore previous") || strings.Contains(turns[0].Directive, "a.go") {
		t.Fatalf("sealed body carried model-derived text: %q", turns[0].Directive)
	}
	if !strings.Contains(turns[0].Content, "ignore previous") || !strings.Contains(turns[0].Content, "not instructions") {
		t.Fatalf("note should ride the plain content, labelled: %q", turns[0].Content)
	}
}

func TestPlanLedgerNoteReadsDedupesAndBounds(t *testing.T) {
	var l planLedger
	var turns []run.Turn
	for i := range maxPlanReadPaths + 5 {
		p := "f" + strings.Repeat("x", i%3) + ".go"
		if i >= 3 {
			p = "file" + string(rune('a'+i%26)) + strings.Repeat("y", i) + ".go"
		}
		turns = append(turns, run.Turn{Role: "assistant", ToolCalls: []rolemanager.ToolCall{{Name: "Read", RawArgs: `{"file_path":"` + p + `"}`}}})
	}
	turns = append(turns, run.Turn{Role: "assistant", ToolCalls: []rolemanager.ToolCall{{Name: "Grep", RawArgs: `{"path":"ignored.go"}`}}})
	l.noteReads(turns)
	if len(l.read) != maxPlanReadPaths {
		t.Fatalf("read = %d, want the cap %d", len(l.read), maxPlanReadPaths)
	}
	if slices.Contains(l.read, "ignored.go") {
		t.Fatal("only read tools count as reads")
	}
	l.noteReads(turns[:1])
	if len(l.read) != maxPlanReadPaths {
		t.Fatal("a repeated read must not be recorded twice")
	}
}

// Advertising is not enforcement: a hallucinated Read on the final pass is
// refused at execution too.
func TestFinalPlanPassRefusesExplorationAtExecution(t *testing.T) {
	root := t.TempDir()
	reg := tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024}, tools.ExitPlanMode{}, tools.UpdatePlan{})
	s := &Session{registry: reg, planMode: true, planFinalPass: true}
	if tool, refusal := s.execTool("Read"); tool != nil || !strings.Contains(refusal, "final planning pass") {
		t.Fatalf("Read on the final pass: tool=%v refusal=%q", tool, refusal)
	}
	if tool, _ := s.execTool("ExitPlanMode"); tool == nil {
		t.Fatal("ExitPlanMode must stay callable on the final pass")
	}
	s.planFinalPass = false
	if tool, _ := s.execTool("Read"); tool == nil {
		t.Fatal("Read is refused only on the final pass")
	}
}

// A plan pass reads at most planPassIterations rounds, whatever the general
// iteration budget. At 40 rounds a planning pass read 114–138 files over 20+
// minutes and never wrote a plan (session b3a026a4).
func TestPlanPassBudgetIsCapped(t *testing.T) {
	s := &Session{maxIter: 40}
	if got := s.passBudget(modes.ModePlan); got != planPassIterations {
		t.Fatalf("plan pass budget = %d, want %d", got, planPassIterations)
	}
	if got := s.passBudget(modes.ModeGoal); got != 40 {
		t.Fatalf("goal pass budget = %d, want the session budget 40", got)
	}
	s.maxIter = 5
	if got := s.passBudget(modes.ModePlan); got != 5 {
		t.Fatalf("a smaller session budget must win, got %d", got)
	}
}

// A pass that spends its read budget without drafting a plan makes the next
// pass the final, write-only one, instead of the evaluator buying another
// pass of reading.
func TestPlanPassLoopWritesAfterABudgetWithNoDraft(t *testing.T) {
	srv, _, _ := planPassServer(t, planPassOpts{eval: []string{"PLAN_PARTIAL", "PLAN_PARTIAL", "PLAN_PARTIAL"}})
	defer srv.Close()
	sess := newPlanPassSession(t, srv, true, 2)
	sess.settings.Resilience = &config.ResilienceSettings{MaxPasses: 5}

	var warned bool
	res, err := sess.run(context.Background(), nil, TurnInput{Prompt: "write me a plan", ForceMode: modes.ModePlan}, false, func(e Event) {
		if e.Kind == EventWarningKind && strings.Contains(e.Warning, "no plan drafted") {
			warned = true
		}
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Passes != 2 {
		t.Fatalf("Passes = %d, want 2: one read pass, then the write-only pass", res.Passes)
	}
	if !warned {
		t.Fatal("the forced write pass was not surfaced")
	}
}

// A planning pass is told, from the harness, when its reading is nearly spent:
// once with the count left and once on the last round, never on the first
// round and never outside plan mode (session d9292a3d read to the last round).
func TestPlanBudgetNudge(t *testing.T) {
	const budget = 12
	var nudged []int
	for i := 0; i < budget; i++ {
		if planBudgetNudge(modes.ModePlan, i, budget) != "" {
			nudged = append(nudged, i)
		}
	}
	if !slices.Equal(nudged, []int{budget - planNudgeRoundsLeft, budget - 1}) {
		t.Fatalf("nudged rounds = %v, want the %dth-from-last and the last", nudged, planNudgeRoundsLeft)
	}
	if got := planBudgetNudge(modes.ModePlan, budget-planNudgeRoundsLeft, budget); !strings.Contains(got, "ExitPlanMode") || !strings.Contains(got, "8 of 12") {
		t.Fatalf("early nudge = %q", got)
	}
	if got := planBudgetNudge(modes.ModePlan, budget-1, budget); !strings.Contains(got, "last tool round") {
		t.Fatalf("last nudge = %q", got)
	}
	for _, m := range []modes.Mode{modes.ModeAgent, modes.ModeGoal} {
		for i := 0; i < budget; i++ {
			if got := planBudgetNudge(m, i, budget); got != "" {
				t.Fatalf("mode %s round %d nudged: %q", m, i, got)
			}
		}
	}
	if planBudgetNudge(modes.ModePlan, 0, 1) != "" {
		t.Fatal("the first round is never nudged")
	}
}

// A non-plan reply is not recorded as the plan (session d9292a3d recorded a
// stray sentence); a real plan is.
func TestRecordPlanSkipsNonPlanReply(t *testing.T) {
	root := t.TempDir()
	s := &Session{workdir: root, allowPassLoop: true}
	var warned string
	emit := func(e Event) {
		if e.Kind == EventWarningKind {
			warned = e.Warning
		}
	}
	res := s.recordPlan(run.Result{Reply: "Updated plan status now.", PlanSentinel: rolemanager.PlanPartial}, "do the thing", emit)
	if res.PlanPath != "" || !strings.Contains(warned, "no plan file recorded") {
		t.Fatalf("non-plan reply recorded: path=%q warning=%q", res.PlanPath, warned)
	}
	plan := "## Summary\n\nx\n\n## Key Changes\n\n1. Do it\n   - Files: a.go\n   - Verify: go test ./...\n"
	res = s.recordPlan(run.Result{PlanText: plan, PlanSentinel: rolemanager.PlanComplete}, "do the thing", emit)
	if res.PlanPath == "" {
		t.Fatal("a real plan must be recorded")
	}
	data, err := os.ReadFile(res.PlanPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "# Plan: do the thing\n") {
		t.Fatalf("recorded plan has no harness title:\n%s", data)
	}
}

// The evaluator can call a finished research checklist complete. With no plan
// text anywhere, the loop must not accept that: it schedules a write pass and
// the plan arrives through ExitPlanMode.
func TestPlanPassLoopRejectsCompleteVerdictWithoutAPlan(t *testing.T) {
	srv, _, _ := planPassServer(t, planPassOpts{main: "reply", reply: "I have researched everything.", eval: []string{"PLAN_COMPLETE", "PLAN_COMPLETE"}})
	defer srv.Close()
	root := t.TempDir()
	sess, err := NewSession(Options{
		Cfg:           run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "test-key", Model: "test"},
		Client:        srv.Client(),
		Registry:      tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024}, tools.ExitPlanMode{}, tools.UpdatePlan{}),
		Posture:       posture.Defaults(),
		AllowPassLoop: true,
		MaxIterations: 2,
		Workdir:       root,
		Settings:      config.Settings{Resilience: &config.ResilienceSettings{MaxPasses: 3}},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	res, err := sess.Run(context.Background(), "write me a plan")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Passes < 2 {
		t.Fatalf("passes = %d: a plan-less PLAN_COMPLETE ended the turn at once", res.Passes)
	}
	if res.PlanPath != "" {
		t.Fatalf("a non-plan reply was recorded as the plan: %s", res.PlanPath)
	}
}

// A model that writes the finished plan as its reply, with a line of narration
// before it, completes the turn on that pass: no evaluator verdict is spent
// (the scripted one would say PLAN_PARTIAL), and the recorded file starts at
// the plan's title.
func TestPlanPassLoopAcceptsAPlanDocumentWrittenAsTheReply(t *testing.T) {
	reply := "I have all the information needed. Grounding complete.\n\n# Add PATCH\n\n## Summary\n\nAdd it.\n\n## Key Changes\n\n1. Change a\n   - Files: a.go\n   - Verify: go test ./...\n\n## Assumptions\n\n- none\n"
	srv, _, _ := planPassServer(t, planPassOpts{main: "reply", reply: reply, eval: []string{"PLAN_PARTIAL", "PLAN_PARTIAL"}})
	defer srv.Close()
	root := t.TempDir()
	sess, err := NewSession(Options{
		Cfg:           run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "test-key", Model: "test"},
		Client:        srv.Client(),
		Registry:      tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024}, tools.ExitPlanMode{}, tools.UpdatePlan{}),
		Posture:       posture.Defaults(),
		AllowPassLoop: true,
		MaxIterations: 3,
		Workdir:       root,
		Settings:      config.Settings{Resilience: &config.ResilienceSettings{MaxPasses: 3}},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	res, err := sess.Run(context.Background(), "add patch")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Passes != 1 || res.PlanSentinel != rolemanager.PlanComplete {
		t.Fatalf("passes = %d, sentinel = %q: a plan document in the reply should complete the turn at once", res.Passes, res.PlanSentinel)
	}
	if strings.Contains(res.PlanText, "Grounding") || !strings.HasPrefix(res.PlanText, "# Add PATCH") {
		t.Fatalf("PlanText = %q", res.PlanText)
	}
	data, err := os.ReadFile(res.PlanPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "Grounding") || !strings.HasPrefix(string(data), "# Add PATCH") {
		t.Fatalf("recorded plan:\n%s", data)
	}
}

// A flawed plan is sent back once per turn with each flaw named; the next
// ExitPlanMode of the turn, flawed or not, is accepted.
func TestLintPlanOnceSendsAFlawedPlanBackOnce(t *testing.T) {
	flawed := "# T\n\n## Summary\n\nx\n\n## Key Changes\n\n1. Code\n   - Files: `src/a.ts`\n   - Verify: `npx vitest run test/a.test.ts`\n2. Tests\n   - Files: `test/a.test.ts` (new)\n   - Verify: `npx vitest run test/a.test.ts`\n"
	s := &Session{}
	back := s.lintPlanOnce(map[string]any{"plan": flawed}, false)
	if !strings.Contains(back, "plan not accepted yet") || !strings.Contains(back, "step 1: Verify runs test/a.test.ts, which step 2 creates") {
		t.Fatalf("first call = %q", back)
	}
	if again := s.lintPlanOnce(map[string]any{"plan": flawed}, false); again != "" {
		t.Fatalf("a plan is sent back once a turn, got %q", again)
	}
	clean := &Session{}
	if got := clean.lintPlanOnce(map[string]any{"plan": "# T\n\n## Summary\n\nx\n\n## Key Changes\n\n1. A\n   - Files: `a.go`\n   - Verify: `go build ./...`\n"}, false); got != "" {
		t.Fatalf("clean plan sent back: %q", got)
	}
	if clean.planLinted {
		t.Fatal("a clean plan must not spend the one rejection")
	}
}

// The permission decision is told, by the harness, when a call targets a file
// the approved plan lists, and only then (measured: with only "Execute the
// approved plan." it denied the plan's own edit).
func TestApprovedPlanFactNamesOnlyListedTargets(t *testing.T) {
	s := &Session{workdir: "/w", turnExecutePlan: true, turnPlanPaths: []string{"main.go", "src/a.ts"}}
	for _, subject := range []string{"main.go", "./main.go", "/w/main.go", "src/a.ts"} {
		if got := s.approvedPlanFact(subject); !strings.Contains(got, "one of the files that plan lists") {
			t.Errorf("%q: %q", subject, got)
		}
	}
	for _, subject := range []string{"other.go", "/etc/passwd", "../main.go", "rm -rf main.go", ""} {
		if got := s.approvedPlanFact(subject); got != "" {
			t.Errorf("%q must get no fact, got %q", subject, got)
		}
	}
	if got := (&Session{workdir: "/w", turnPlanPaths: []string{"main.go"}}).approvedPlanFact("main.go"); got != "" {
		t.Errorf("a turn that is not executing a plan gets no fact: %q", got)
	}
}

// The last round of any plan pass offers only the finishing tools, so a pass
// that read to its last round writes the plan there instead of costing a whole
// extra pass (reproducing session d9292a3d: five of six runs spent a pass this
// way). Earlier rounds keep the reading tools.
func TestPlanPassLastRoundOffersOnlyTheFinishTools(t *testing.T) {
	srv, mu, toolSets, _ := planFinalPassServer(t, "PLAN_PARTIAL\nMissing: x")
	defer srv.Close()
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o600)
	sess, err := NewSession(Options{
		Cfg:           run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "test-key", Model: "test"},
		Client:        srv.Client(),
		Registry:      tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024}, tools.ExitPlanMode{}, tools.UpdatePlan{}),
		Posture:       posture.Defaults(),
		AllowPassLoop: true,
		MaxIterations: 3,
		Workdir:       root,
		Settings:      config.Settings{Resilience: &config.ResilienceSettings{MaxPasses: 3}},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	res, err := sess.Run(context.Background(), "write me a plan")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.PlanSentinel != rolemanager.PlanComplete || res.Passes != 1 {
		t.Fatalf("passes = %d, sentinel = %q: the plan should arrive on the first pass's last round", res.Passes, res.PlanSentinel)
	}
	mu.Lock()
	defer mu.Unlock()
	sets := *toolSets
	if len(sets) != 3 {
		t.Fatalf("requests = %d, want 3 rounds", len(sets))
	}
	if !slices.Contains(sets[0], "Read") || !slices.Contains(sets[1], "Read") {
		t.Fatalf("rounds before the last keep Read: %v %v", sets[0], sets[1])
	}
	last := append([]string(nil), sets[2]...)
	slices.Sort(last)
	if !slices.Equal(last, []string{"ExitPlanMode"}) {
		t.Fatalf("last round tools = %v", last)
	}
	if sess.planFinalPass {
		t.Fatal("the narrowing must not outlive the pass")
	}
}

// A flawed plan on a pass's last round, or on the finishing pass, is accepted:
// sending it back would spend the last round the planner has.
func TestLintPlanOnceAcceptsOnTheLastRound(t *testing.T) {
	flawed := "# T\n\n## Summary\n\nx\n\n## Key Changes\n\n1. A\n   - Verify: go build\n"
	s := &Session{}
	if got := s.lintPlanOnce(map[string]any{"plan": flawed}, true); got != "" {
		t.Fatalf("last round sent a plan back: %q", got)
	}
	if s.planLinted {
		t.Fatal("the unspent rejection must stay available")
	}
}

// The lint checks the plan's paths against the session's own repository.
func TestLintPlanOnceChecksPathsAgainstTheWorkdir(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "real.go"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Session{workdir: root}
	ghost := "# T\n\n## Summary\n\nx\n\n## Key Changes\n\n1. A\n   - Files: `real.go`, `docs/ghost.md`\n   - Verify: none\n"
	back := s.lintPlanOnce(map[string]any{"plan": ghost}, false)
	if !strings.Contains(back, "docs/ghost.md, which does not exist") || strings.Contains(back, "real.go,") {
		t.Fatalf("lint reply = %q", back)
	}
	ok := "# T\n\n## Summary\n\nx\n\n## Key Changes\n\n1. A\n   - Files: `real.go`, `docs/ghost.md` (new)\n   - Verify: none\n"
	if got := (&Session{workdir: root}).lintPlanOnce(map[string]any{"plan": ok}, false); got != "" {
		t.Fatalf("a marked-new file was flagged: %q", got)
	}
}

// A large plan with no mechanical flaw is sent back once with the review
// checklist; a small one is not.
func TestLintPlanOnceReviewsLargePlans(t *testing.T) {
	var b strings.Builder
	b.WriteString("# T\n\n## Summary\n\nx\n\n## Key Changes\n\n")
	for i := 1; i <= 5; i++ {
		fmt.Fprintf(&b, "%d. Step\n   - Files: `f%d.go` (new)\n   - Verify: none\n", i, i)
	}
	s := &Session{}
	back := s.lintPlanOnce(map[string]any{"plan": b.String()}, false)
	if !strings.Contains(back, "plan check before it is accepted") {
		t.Fatalf("a five-step plan should get the checklist, got %q", back)
	}
	if again := s.lintPlanOnce(map[string]any{"plan": b.String()}, false); again != "" {
		t.Fatalf("the checklist is sent once, got %q", again)
	}
	small := "# T\n\n## Summary\n\nx\n\n## Key Changes\n\n1. A\n   - Files: `a.go` (new)\n   - Verify: none\n"
	if got := (&Session{}).lintPlanOnce(map[string]any{"plan": small}, false); got != "" {
		t.Fatalf("a one-step plan must not be reviewed: %q", got)
	}
}

// A plan sent back on a pass's last round is given one more round, so the
// correction lands in the same pass instead of costing a whole new one.
func TestPlanSentBackOnTheLastRoundIsCorrectedInTheSamePass(t *testing.T) {
	var mu sync.Mutex
	var rounds [][]string
	exits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
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
			var names []string
			for _, tl := range req.Tools {
				names = append(names, tl.Function.Name)
			}
			if len(names) == 0 {
				writeChatJSON(w, "name")
				return
			}
			mu.Lock()
			rounds = append(rounds, names)
			mu.Unlock()
			if slices.Contains(names, "Read") {
				writeToolCallJSON(w, "Read", `{"file_path":"f.txt"}`)
				return
			}
			mu.Lock()
			exits++
			n := exits
			mu.Unlock()
			if n == 1 {
				// No Files line: the lint sends this back.
				writeToolCallJSON(w, "ExitPlanMode", `{"plan":"# T\n## Summary\nx\n## Key Changes\n1. change it\n   - Verify: go build ./..."}`)
				return
			}
			writeToolCallJSON(w, "ExitPlanMode", `{"plan":"# T\n## Summary\nfixed\n## Key Changes\n1. change it\n   - Files: p.go\n   - Verify: go build ./..."}`)
		}
	}))
	defer srv.Close()
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o600)
	sess, err := NewSession(Options{
		Cfg:           run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "test-key", Model: "test"},
		Client:        srv.Client(),
		Registry:      tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024}, tools.ExitPlanMode{}, tools.UpdatePlan{}),
		Posture:       posture.Defaults(),
		AllowPassLoop: true,
		MaxIterations: 3,
		Workdir:       root,
		Settings:      config.Settings{Resilience: &config.ResilienceSettings{MaxPasses: 3}},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	res, err := sess.Run(context.Background(), "write me a plan")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Passes != 1 || res.PlanSentinel != rolemanager.PlanComplete || !strings.Contains(res.PlanText, "fixed") {
		t.Fatalf("passes = %d, sentinel = %q, plan = %q: the correction should land in the first pass", res.Passes, res.PlanSentinel, res.PlanText)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(rounds) != 4 {
		t.Fatalf("requests = %d (%v), want 3 rounds plus one extra", len(rounds), rounds)
	}
	for i, names := range rounds[2:] {
		if slices.Contains(names, "Read") {
			t.Fatalf("round %d still offers Read: %v", i+3, names)
		}
	}
}

// A plan the lint sent back is still recorded when the correction never comes:
// measured in a large-prompt run whose model, given one more round, called a
// tool the finishing surface withholds and ended the turn with no plan at all.
func TestRejectedPlanIsRecordedWhenNoCorrectionArrives(t *testing.T) {
	var mu sync.Mutex
	exits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
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
			if len(req.Tools) == 0 {
				writeChatJSON(w, "name")
				return
			}
			var names []string
			for _, tl := range req.Tools {
				names = append(names, tl.Function.Name)
			}
			if slices.Contains(names, "Read") {
				writeToolCallJSON(w, "Read", `{"file_path":"f.txt"}`)
				return
			}
			mu.Lock()
			exits++
			n := exits
			mu.Unlock()
			if n == 1 {
				writeToolCallJSON(w, "ExitPlanMode", `{"plan":"# T\n## Summary\nthe rejected plan\n## Key Changes\n1. change it\n   - Verify: go build ./..."}`)
				return
			}
			// The correction never comes: a tool the narrowed surface withholds.
			writeToolCallJSON(w, "Read", `{"file_path":"f.txt"}`)
		}
	}))
	defer srv.Close()
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o600)
	sess, err := NewSession(Options{
		Cfg:           run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "test-key", Model: "test"},
		Client:        srv.Client(),
		Registry:      tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024}, tools.ExitPlanMode{}, tools.UpdatePlan{}),
		Posture:       posture.Defaults(),
		AllowPassLoop: true,
		MaxIterations: 3,
		Workdir:       root,
		Settings:      config.Settings{Resilience: &config.ResilienceSettings{MaxPasses: 2}},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	res, err := sess.Run(context.Background(), "write me a plan")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.PlanPath == "" {
		t.Fatalf("no plan recorded; reply = %q", res.Reply)
	}
	data, err := os.ReadFile(res.PlanPath)
	if err != nil || !strings.Contains(string(data), "the rejected plan") {
		t.Fatalf("the rejected plan should be what is recorded: %v\n%s", err, data)
	}
}

// A last round spent on update_plan, which the finishing surface withholds, is
// given back once, so the plan is written in the same pass (measured: one run
// of the failing session's prompt spent pass 1 this way and needed a second).
func TestWithheldToolOnTheLastRoundCostsNoPass(t *testing.T) {
	var mu sync.Mutex
	narrowed := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
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
			if len(req.Tools) == 0 {
				writeChatJSON(w, "name")
				return
			}
			var names []string
			for _, tl := range req.Tools {
				names = append(names, tl.Function.Name)
			}
			if slices.Contains(names, "Read") {
				writeToolCallJSON(w, "Read", `{"file_path":"f.txt"}`)
				return
			}
			mu.Lock()
			narrowed++
			n := narrowed
			mu.Unlock()
			if n == 1 {
				writeToolCallJSON(w, "update_plan", `{"plan":[{"step":"read","status":"completed"}]}`)
				return
			}
			writeToolCallJSON(w, "ExitPlanMode", `{"plan":"# T\n## Summary\nthe plan\n## Key Changes\n1. change it\n   - Files: p.go (new)\n   - Verify: go build ./..."}`)
		}
	}))
	defer srv.Close()
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o600)
	sess, err := NewSession(Options{
		Cfg:           run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "test-key", Model: "test"},
		Client:        srv.Client(),
		Registry:      tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024}, tools.ExitPlanMode{}, tools.UpdatePlan{}),
		Posture:       posture.Defaults(),
		AllowPassLoop: true,
		MaxIterations: 3,
		Workdir:       root,
		Settings:      config.Settings{Resilience: &config.ResilienceSettings{MaxPasses: 3}},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	res, err := sess.Run(context.Background(), "write me a plan")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Passes != 1 || !strings.Contains(res.PlanText, "the plan") {
		t.Fatalf("passes = %d, narrowed = %d, plan = %q: the plan should land in the first pass", res.Passes, narrowed, res.PlanText)
	}
}

// A last round spent on update_plan and then a plan the lint sends back still
// ends in the first pass: two rounds are given back.

func TestWithheldToolThenRejectedPlanStillLandsInThePass(t *testing.T) {
	var mu sync.Mutex
	narrowed := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
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
			if len(req.Tools) == 0 {
				writeChatJSON(w, "name")
				return
			}
			var names []string
			for _, tl := range req.Tools {
				names = append(names, tl.Function.Name)
			}
			if slices.Contains(names, "Read") {
				writeToolCallJSON(w, "Read", `{"file_path":"f.txt"}`)
				return
			}
			mu.Lock()
			narrowed++
			n := narrowed
			mu.Unlock()
			if n == 2 {
				writeToolCallJSON(w, "ExitPlanMode", `{"plan":"# T\n## Summary\nx\n## Key Changes\n1. change it\n   - Verify: go build ./..."}`)
				return
			}
			if n == 1 {
				writeToolCallJSON(w, "update_plan", `{"plan":[{"step":"read","status":"completed"}]}`)
				return
			}
			writeToolCallJSON(w, "ExitPlanMode", `{"plan":"# T\n## Summary\nthe plan\n## Key Changes\n1. change it\n   - Files: p.go (new)\n   - Verify: go build ./..."}`)
		}
	}))
	defer srv.Close()
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o600)
	sess, err := NewSession(Options{
		Cfg:           run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "test-key", Model: "test"},
		Client:        srv.Client(),
		Registry:      tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024}, tools.ExitPlanMode{}, tools.UpdatePlan{}),
		Posture:       posture.Defaults(),
		AllowPassLoop: true,
		MaxIterations: 3,
		Workdir:       root,
		Settings:      config.Settings{Resilience: &config.ResilienceSettings{MaxPasses: 3}},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	res, err := sess.Run(context.Background(), "write me a plan")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Passes != 1 || !strings.Contains(res.PlanText, "the plan") {
		t.Fatalf("passes = %d, narrowed = %d, plan = %q: the plan should land in the first pass", res.Passes, narrowed, res.PlanText)
	}
}
