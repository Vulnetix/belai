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
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/modes"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/repomap"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/todos"
	"github.com/vulnetix/signet/internal/tools"
)

// goalPassOpts configures the scripted goal-pass mock server.
type goalPassOpts struct {
	mode  string   // operating-mode classifier reply (default "GOAL")
	eval  []string // goal-evaluator sentinel sequence
	main  string   // main model behaviour: "tool" | "length" | "reply" | "read"
	reply string   // main model final reply when main == "reply"
	// report is the main model's reply to the final report directive; when
	// empty the report turn gets the ordinary main-model behaviour. The
	// sentinel value "FAIL" answers the report turn with a provider error.
	report string
}

// isReportTurn reports whether the request's last user message carries a
// final report directive.
func isReportTurn(lastUser string) bool {
	return strings.Contains(lastUser, "Write the final report") || strings.Contains(lastUser, "Write a report for the user")
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
	writeIdx := 0
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
		var system, lastUser string
		for _, m := range req.Messages {
			switch m.Role {
			case "system":
				system = m.Content
			case "user":
				lastUser = m.Content
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
		case strings.Contains(system, "goal-contract writer"):
			writeChatJSON(w, "## Verification surface\nRun the test suite and confirm it passes.\n\n## Constraints\nKeep the repo building and tests green.\n\n## Boundaries\nOnly edit files under the working directory.\n\n## Iteration policy\nMake one concrete change per pass and re-run tests.\n\n## Blocked stop condition\nStop only if a required input is missing.")
		default:
			mu.Lock()
			mainSystems[system] = true
			mu.Unlock()
			if opts.report != "" && isReportTurn(lastUser) {
				if opts.report == "FAIL" {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":{"message":"report rejected"}}`))
					return
				}
				writeChatJSON(w, opts.report)
				return
			}
			switch opts.main {
			case "length":
				writeLengthRepairJSON(w, "Read", `{"path":"f.txt"}`)
			case "write-then-length":
				// One real write, then nothing but truncation repair: the
				// goal has work on disk when the empty passes start.
				mu.Lock()
				writeIdx++
				n := writeIdx
				mu.Unlock()
				if n == 1 {
					writeToolCallWithContentJSON(w, "Write", `{"path":"f.txt","content":"written\n"}`, "Plan:\n1. Ship the release\n[DONE:1]\n")
					return
				}
				writeLengthRepairJSON(w, "Read", `{"path":"f.txt"}`)
			case "reply":
				writeChatJSON(w, opts.reply)
			default:
				// Include a completed todo item in the assistant text so
				// verification has work to check; an all-pending list would
				// skip the verification pass and change pass-loop timing.
				switch opts.main {
				case "tool-nocontent":
					writeToolCallJSON(w, "Read", `{"path":"f.txt"}`)
				case "update_plan":
					// A pass whose only successful call is update_plan. It
					// changes no file, but it did execute a tool, so it must
					// not read as "executed no tools".
					writeToolCallJSON(w, "update_plan", `{"plan":[{"step":"ship it","status":"in_progress"}]}`)
				case "read":
					// A pass that only reads: the loop observes no file
					// change, which is what drives the no-write escalation.
					writeToolCallWithContentJSON(w, "Read", `{"path":"f.txt"}`, "Plan:\n1. Ship the release\n[DONE:1]\n")
				default:
					// The default pass writes a file, because that is what a
					// goal-mode pass is supposed to do: the pass loop's
					// progress signal is the file-diff recorder, not the
					// assistant's prose.
					mu.Lock()
					writeIdx++
					n := writeIdx
					mu.Unlock()
					args := fmt.Sprintf(`{"path":"f.txt","content":%q}`, fmt.Sprintf("pass %d\n", n))
					writeToolCallWithContentJSON(w, "Write", args, "Plan:\n1. Ship the release\n[DONE:1]\n")
				}
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
		Cfg:      cfg,
		Client:   srv.Client(),
		Workdir:  root,
		Registry: tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024}, &tools.Write{Root: root, MaxBytes: tools.MaxWriteBytes, Cwd: tools.NewCwd(root)}, tools.UpdatePlan{}),
		Posture:  posture.Defaults(),
		// The scripted passes write, and a write without a TTY would
		// otherwise be withheld by the permission-ask gate.
		AskDisabled:   true,
		AllowExplore:  true,
		AllowPassLoop: allowPassLoop,
		MaxIterations: maxIter,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return sess
}

// An all-withheld goal must terminate with the tool failure named, not loop
// forever or pretend the model is simply not writing.
func TestGoalPassLoopTerminatesOnAllWithheld(t *testing.T) {
	srv, _, _ := goalPassServer(t, goalPassOpts{mode: "GOAL", main: "tool"})
	defer srv.Close()

	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o600)
	cwd := tools.NewCwd(root)
	cfg := run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "test-key", Model: "test"}
	sess, err := NewSession(Options{
		Cfg:      cfg,
		Client:   srv.Client(),
		Workdir:  root,
		Registry: tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024, Cwd: cwd}, &tools.Write{Root: root, Cwd: cwd}),
		Posture:  posture.Defaults(),
		// No AskDisabled: the permission gate withholds the Write, which is
		// exactly the all-withheld condition this test exercises.
		AllowPassLoop: true,
		MaxIterations: 1,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	_, err = sess.Run(context.Background(), "ship the thing")
	if err == nil {
		t.Fatal("expected the all-withheld tool-failure error")
	}
	if !strings.Contains(err.Error(), "all tool results withheld") {
		t.Fatalf("error = %v, want the all-withheld tool failure", err)
	}
}

func TestGoalPassLoopPartialPartialComplete(t *testing.T) {
	// The sealed system prompt is deliberately not byte-identical across
	// passes — the harness re-seals it with fresh nonces — so this asserts the
	// loop's outcome, which is what the test is for. It was skipped wholesale
	// for the prompt-identity assertion alone, leaving the multi-pass
	// PARTIAL -> PARTIAL -> COMPLETE path uncovered.
	srv, _, _ := goalPassServer(t, goalPassOpts{eval: []string{"GOAL_PARTIAL", "GOAL_PARTIAL", "GOAL_COMPLETE"}})
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

// A reasoning-wrapped sentinel is a real verdict, not a malformed reply:
// the evaluator normalizes away the thinking block and reads GOAL_COMPLETE.
func TestGoalPassLoopAcceptsReasoningWrappedSentinel(t *testing.T) {
	srv, _, _ := goalPassServer(t, goalPassOpts{eval: []string{"<thinking>…</thinking>GOAL_COMPLETE", "GOAL_COMPLETE"}})
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
		t.Fatalf("Passes = %d, want 2", res.Passes)
	}
}

// A broken evaluator ends the loop, but it must not throw the work away: the
// passes that ran produced real changes on disk, and a garbled classifier
// token is no reason to return an error instead of them. Each malformed reply
// costs two evaluator calls, because the first is re-asked with the exact
// syntax it broke before it counts against the streak.
func TestGoalPassLoopTwoMalformedEvaluationsStopGracefully(t *testing.T) {
	srv, _, _ := goalPassServer(t, goalPassOpts{eval: []string{"garbage", "still garbage", "more garbage", "garbage again"}})
	defer srv.Close()
	sess := newGoalPassSession(t, srv, true, 2)

	var sawMalformed bool
	var warning string
	res, err := sess.run(context.Background(), nil, TurnInput{Prompt: "ship the thing"}, false, func(e Event) {
		if e.Kind == EventGoalEvalKind && e.Malformed {
			sawMalformed = true
		}
		if e.Kind == EventWarningKind {
			warning = e.Warning
		}
	})
	if err != nil {
		t.Fatalf("a broken evaluator must return the work so far, not an error: %v", err)
	}
	if res.Passes != 2 {
		t.Fatalf("Passes = %d, want 2 (one pass per malformed verdict)", res.Passes)
	}
	if res.GoalSentinel != rolemanager.GoalPartial {
		t.Fatalf("GoalSentinel = %q, want the fail-closed GOAL_PARTIAL", res.GoalSentinel)
	}
	if !sawMalformed {
		t.Fatal("the malformed evaluator reply must be reported with Malformed set on the event")
	}
	if !strings.Contains(warning, "malformed") {
		t.Fatalf("warning = %q, want the broken-evaluator warning", warning)
	}
}

func TestGoalPassLoopZeroProductiveDoesNotLoop(t *testing.T) {
	// finish_reason "length" withholds every tool result, so each pass is
	// unproductive. The first one is repaired; the second ends the loop.
	srv, _, _ := goalPassServer(t, goalPassOpts{main: "length", eval: []string{"GOAL_PARTIAL"}})
	defer srv.Close()
	sess := newGoalPassSession(t, srv, true, 2)

	_, err := sess.Run(context.Background(), "ship the thing")
	if err == nil || !strings.Contains(err.Error(), "no tools") {
		t.Fatalf("expected zero-productive termination, got %v", err)
	}
}

// One unproductive pass is repairable: every call rejected before it ran is a
// bad argument shape, not the end of the run, and failing there discards the
// passes that did work. The loop injects the repair directive and tries once
// more before it gives up.
func TestGoalPassLoopRepairsOneUnproductivePassBeforeStopping(t *testing.T) {
	srv, _, _ := goalPassServer(t, goalPassOpts{main: "length", eval: []string{"GOAL_PARTIAL"}})
	defer srv.Close()
	sess := newGoalPassSession(t, srv, true, 2)

	var warnings []string
	res, err := sess.run(context.Background(), nil, TurnInput{Prompt: "ship the thing"}, false, func(e Event) {
		if e.Kind == EventWarningKind {
			warnings = append(warnings, e.Warning)
		}
	})
	if err == nil || !strings.Contains(err.Error(), "no tools") {
		t.Fatalf("a second empty pass must stop the loop, got %v", err)
	}
	if res.Passes != maxUnproductivePasses {
		t.Fatalf("Passes = %d, want %d (one repair attempt before stopping)", res.Passes, maxUnproductivePasses)
	}
	if len(warnings) == 0 || !strings.Contains(warnings[0], "corrected tool calls") {
		t.Fatalf("warnings = %v, want the repair notice on the first empty pass", warnings)
	}
}

// A goal that already changed files keeps its work when the empty passes
// arrive: the edits are on disk either way, and an error would throw away the
// reply describing them.
func TestGoalPassLoopKeepsWorkWhenUnproductiveAfterWriting(t *testing.T) {
	srv, _, _ := goalPassServer(t, goalPassOpts{main: "write-then-length", eval: []string{"GOAL_PARTIAL", "GOAL_PARTIAL", "GOAL_PARTIAL"}})
	defer srv.Close()
	sess := newGoalPassSession(t, srv, true, 2)

	var warnings []string
	res, err := sess.run(context.Background(), nil, TurnInput{Prompt: "ship the thing"}, false, func(e Event) {
		if e.Kind == EventWarningKind {
			warnings = append(warnings, e.Warning)
		}
	})
	if err != nil {
		t.Fatalf("a goal with work on disk must return it, not an error: %v", err)
	}
	if res.GoalSentinel != rolemanager.GoalPartial {
		t.Fatalf("GoalSentinel = %q, want GOAL_PARTIAL", res.GoalSentinel)
	}
	var kept bool
	for _, w := range warnings {
		if strings.Contains(w, "returning the work so far") {
			kept = true
		}
	}
	if !kept {
		t.Fatalf("warnings = %v, want the work-kept notice", warnings)
	}
}

// update_plan is bookkeeping, but a pass that called it did execute a tool.
// Counting it as an empty pass used to fail the whole goal with "executed no
// tools" even though the call succeeded.
func TestGoalPassLoopCountsUpdatePlanAsExecutedWork(t *testing.T) {
	srv, _, _ := goalPassServer(t, goalPassOpts{main: "update_plan", eval: []string{"GOAL_PARTIAL", "GOAL_PARTIAL"}})
	defer srv.Close()
	sess := newGoalPassSession(t, srv, true, 2)
	sess.settings = config.Settings{Resilience: &config.ResilienceSettings{MaxPasses: 2}}

	_, err := sess.Run(context.Background(), "ship the thing")
	if err == nil {
		t.Fatal("the ceiling must stop this loop")
	}
	if strings.Contains(err.Error(), "no tools") {
		t.Fatalf("a successful update_plan must count as executed work, got %v", err)
	}
	if !strings.Contains(err.Error(), "max passes") {
		t.Fatalf("expected the configured ceiling to stop the loop, got %v", err)
	}
}

func TestGoalPassLoopDisabledStillContinues(t *testing.T) {
	srv, _, _ := goalPassServer(t, goalPassOpts{eval: []string{"GOAL_COMPLETE"}})
	defer srv.Close()
	sess := newGoalPassSession(t, srv, false, 3)
	sess.settings = config.Settings{Resilience: &config.ResilienceSettings{MaxPasses: 1}}

	_, err := sess.Run(context.Background(), "ship the thing")
	if err != nil {
		t.Fatalf("budget exhaustion with the pass loop disabled must not be an error, got %v", err)
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

	srv, _, _ := goalPassServer(t, goalPassOpts{main: "tool-nocontent"})
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
	out, _, err := sess.pass(context.Background(), pipe, "", nil, false, func(Event) {}, modes.ModeAgent)
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
	// The session below registers Read alone, so the scripted passes read.
	srv, _, _ := goalPassServer(t, goalPassOpts{main: "read", eval: []string{"GOAL_PARTIAL"}})
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

func TestGoalPassLoopStallTriggersProgression(t *testing.T) {
	// When the loop has not seen todo progress for goalStallPartial passes,
	// it should inject a progression directive and start a new agentic loop
	// instead of aborting. Without a ceiling a stuck loop would now run
	// forever, so we use a small max-passes ceiling to prove the loop
	// survived past the old stall boundary.
	srv, _, _ := goalPassServer(t, goalPassOpts{main: "read", eval: []string{"GOAL_PARTIAL"}})
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
		Settings:      config.Settings{Resilience: &config.ResilienceSettings{MaxPasses: 5}},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	res, err := sess.Run(context.Background(), "ship the thing")
	if err == nil || !strings.Contains(err.Error(), "max passes (5) reached") {
		t.Fatalf("expected max-pass ceiling after progression reset, got %v", err)
	}
	if res.Passes != 5 {
		t.Fatalf("Passes = %d, want 5 (loop should continue past the old stall boundary)", res.Passes)
	}
}

func TestPassLedgerProgressionDirective(t *testing.T) {
	l := passLedger{goalText: "ship the thing"}
	got := l.progressionDirective()
	if !strings.Contains(got, "no step list is tracked yet") {
		t.Fatalf("progression directive without a list should ask for one, got:\n%s", got)
	}
	if !strings.Contains(got, "Progress has stalled") {
		t.Fatalf("progression directive should mention the stall, got:\n%s", got)
	}
	if !strings.Contains(got, "as an edit") {
		t.Fatalf("progression directive should demand an edit, not a report, got:\n%s", got)
	}

	l.list = todos.New("ship the thing", []string{"alpha", "beta"})
	l.hasList = true
	got = l.progressionDirective()
	if strings.Contains(got, "alpha") {
		t.Fatalf("progression body must not carry model step text (the TODO check note does), got:\n%s", got)
	}
	turns := l.directive(got)
	if !strings.Contains(turns[0].Content, "alpha") || !strings.Contains(turns[0].Content, "beta") {
		t.Fatalf("framed progression directive should carry the rendered list, got:\n%s", turns[0].Content)
	}
	if !strings.Contains(turns[0].Directive, "[DONE:n]") || !strings.Contains(turns[0].Directive, "TODO check") {
		t.Fatalf("framed progression directive should carry the TODO check, got:\n%s", turns[0].Directive)
	}
}

func TestPassLedgerNotePartialResets(t *testing.T) {
	l := passLedger{goalText: "g"}
	for i := 0; i < goalStallPartial-1; i++ {
		if l.notePartial() {
			t.Fatalf("notePartial should not trigger on iteration %d", i)
		}
	}
	if !l.notePartial() {
		t.Fatal("notePartial should trigger after goalStallPartial no-progress partials")
	}
	if l.partialStreak != goalStallPartial {
		t.Fatalf("partialStreak = %d, want %d", l.partialStreak, goalStallPartial)
	}
	// A todo transition resets the streak.
	l.advanceTodos("Plan:\n1. one\n2. two\n")
	if l.notePartial() {
		t.Fatal("notePartial should not trigger immediately after a todo transition")
	}
	if l.partialStreak != 0 {
		t.Fatalf("partialStreak = %d, want 0 after reset", l.partialStreak)
	}
}

func TestCompactBoundaryThreshold(t *testing.T) {
	summary := "## Goal\nship\n## Next Steps\n1. build\n## Critical Context\npath=/x"
	classifier := rolemanager.ClassifierFunc(func(_ context.Context, p rolemanager.ClassifierPayload) (string, error) {
		if strings.Contains(p.System, "summarization") {
			return summary, nil
		}
		return "SAFE", nil
	})
	pipe := rolemanager.NewPipeline(classifier)

	s := &Session{
		cfg:      run.Config{Model: "test"},
		settings: config.Settings{ContextWindows: map[string]int{"test": 1000}},
		live:     posture.NewLive(posture.Defaults(), false),
	}
	long := strings.Repeat("a", 8000) // ~2000 estimated tokens
	turns := []run.Turn{
		{Role: "user", Content: "do the thing"},
		{Role: "assistant", Content: long},
		{Role: "tool", Content: long, ToolCallID: "c1", ToolName: "Read"},
	}

	compacted, ok := s.compactBoundary(context.Background(), pipe, turns)
	if !ok {
		t.Fatalf("expected compaction above the 70%% threshold")
	}
	if len(compacted) != 2 {
		t.Fatalf("compacted turns = %d, want 2 (summary user + ack assistant)", len(compacted))
	}

	// Below threshold: no compaction.
	bigWindow := &Session{
		cfg:      run.Config{Model: "test"},
		settings: config.Settings{ContextWindows: map[string]int{"test": 1 << 20}},
		live:     posture.NewLive(posture.Defaults(), false),
	}
	if _, ok := bigWindow.compactBoundary(context.Background(), pipe, turns); ok {
		t.Fatalf("expected no compaction below the threshold")
	}
}

// compactBoundary is best-effort: every failure leaves the turns untouched and
// lets the next boundary try again. The cases below are the documented skips
// in docs/role-manager.md, "Compaction at the boundary".
func TestCompactBoundarySkipsOnFailure(t *testing.T) {
	const summary = "## Goal\nship\n## Next Steps\n1. build\n## Critical Context\npath=/x"
	isCompaction := func(p rolemanager.ClassifierPayload) bool {
		return strings.Contains(p.System, "summarization")
	}
	long := strings.Repeat("a", 8000) // ~2000 estimated tokens
	turns := []run.Turn{
		{Role: "user", Content: "do the thing"},
		{Role: "assistant", Content: long},
		{Role: "tool", Content: long, ToolCallID: "c1", ToolName: "Read"},
	}

	cases := []struct {
		name       string
		window     map[string]int
		classifier rolemanager.ClassifierFunc
	}{
		{
			name:   "unknown context window",
			window: nil,
			classifier: func(_ context.Context, p rolemanager.ClassifierPayload) (string, error) {
				if isCompaction(p) {
					return summary, nil
				}
				return "SAFE", nil
			},
		},
		{
			name:   "classifier transport error",
			window: map[string]int{"test": 1000},
			classifier: func(_ context.Context, p rolemanager.ClassifierPayload) (string, error) {
				return "", errors.New("provider returned 500")
			},
		},
		{
			name:   "summary missing required headings",
			window: map[string]int{"test": 1000},
			classifier: func(_ context.Context, p rolemanager.ClassifierPayload) (string, error) {
				if isCompaction(p) {
					return "just a sentence", nil
				}
				return "SAFE", nil
			},
		},
		{
			name:   "summary refused by the Role Manager",
			window: map[string]int{"test": 1000},
			classifier: func(_ context.Context, p rolemanager.ClassifierPayload) (string, error) {
				if isCompaction(p) {
					return summary, nil
				}
				return "PROMPT_INJECTION", nil
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Session{
				cfg:      run.Config{Model: "test"},
				settings: config.Settings{ContextWindows: tc.window},
				live:     posture.NewLive(posture.Defaults(), false),
			}
			got, ok := s.compactBoundary(context.Background(), rolemanager.NewPipeline(tc.classifier), turns)
			if ok {
				t.Fatalf("compactBoundary compacted anyway: %+v", got)
			}
			if got != nil {
				t.Fatalf("compactBoundary returned turns on failure: %+v", got)
			}
		})
	}
}

// A successful compaction replaces the whole turn list with the wrapped
// summary and its acknowledgement.
func TestCompactBoundaryReplacesTurnsWithSummary(t *testing.T) {
	const summary = "## Goal\nship\n## Next Steps\n1. build\n## Critical Context\npath=/x"
	classifier := rolemanager.ClassifierFunc(func(_ context.Context, p rolemanager.ClassifierPayload) (string, error) {
		if strings.Contains(p.System, "summarization") {
			return summary, nil
		}
		return "SAFE", nil
	})
	s := &Session{
		cfg:      run.Config{Model: "test"},
		settings: config.Settings{ContextWindows: map[string]int{"test": 1000}},
		live:     posture.NewLive(posture.Defaults(), false),
	}
	long := strings.Repeat("a", 8000)
	turns := []run.Turn{
		{Role: "user", Content: "do the thing"},
		{Role: "assistant", Content: long},
	}

	got, ok := s.compactBoundary(context.Background(), rolemanager.NewPipeline(classifier), turns)
	if !ok {
		t.Fatalf("expected compaction above the threshold")
	}
	if len(got) != 2 {
		t.Fatalf("compacted turns = %d, want 2", len(got))
	}
	if got[0].Role != "user" || !strings.Contains(got[0].Content, summary) {
		t.Fatalf("first turn = %+v, want the summary as a user turn", got[0])
	}
	if !strings.HasPrefix(got[0].Content, rolemanager.SummaryPrefix) ||
		!strings.HasSuffix(got[0].Content, rolemanager.SummarySuffix) {
		t.Fatalf("summary turn is not wrapped: %q", got[0].Content)
	}
	if got[1].Role != "assistant" || got[1].Content != rolemanager.SummaryAck {
		t.Fatalf("second turn = %+v, want the summary acknowledgement", got[1])
	}
}

func TestPassLedgerHasVerifiableWork(t *testing.T) {
	cases := []struct {
		name     string
		hasList  bool
		statuses []todos.Status
		want     bool
	}{
		{"no list", false, nil, false},
		{"all pending", true, []todos.Status{todos.StatusActive, todos.StatusPending}, false},
		{"one done", true, []todos.Status{todos.StatusActive, todos.StatusDone}, true},
		{"all done", true, []todos.Status{todos.StatusDone, todos.StatusDone}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := passLedger{goalText: "g", hasList: tc.hasList}
			if tc.hasList {
				items := make([]todos.Item, len(tc.statuses))
				for i, st := range tc.statuses {
					items[i] = todos.Item{N: i + 1, Text: "step", Status: st}
				}
				l.list = todos.List{Items: items}
			}
			if got := l.hasVerifiableWork(); got != tc.want {
				t.Fatalf("hasVerifiableWork() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPassLedgerPartialDirectiveTurnSkipsVerificationWhenNoWorkDone(t *testing.T) {
	// An all-pending list at the verification boundary should not arm
	// verification, because there is nothing completed to verify.
	l := passLedger{goalText: "g", hasList: true, partialStreak: 2}
	l.list = todos.New("g", []string{"alpha", "beta"})
	body, arm := l.partialDirectiveTurn()
	if arm {
		t.Fatalf("verification should not arm for an all-pending list, got body %q", body)
	}
	want := l.partialDirective()
	if body != want {
		t.Fatalf("body = %q, want partialDirective() = %q", body, want)
	}

	// A list with at least one completed item at the boundary arms
	// verification.
	l.list.Items[0].Status = todos.StatusDone
	body, arm = l.partialDirectiveTurn()
	if !arm {
		t.Fatalf("verification should arm when a completed item exists, got body %q", body)
	}
	if body != verificationDirective {
		t.Fatalf("body = %q, want verificationDirective", body)
	}
}

func TestGoalAckDirectiveNamesDefaultVerificationSurface(t *testing.T) {
	// A session with no repo map keeps the bare directive.
	bare, err := NewSession(Options{Cfg: run.Config{Provider: "openai", Model: "test"}, Client: http.DefaultClient})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if got := bare.goalAckDirective(); got != goalAckDirective {
		t.Fatalf("bare directive = %q, want the const", got)
	}

	// A repo map with test commands names them as the default verification
	// surface.
	m := repomap.Map{Commands: repomap.Commands{Test: []string{"just check", "go test ./..."}}}
	sess, err := NewSession(Options{
		Cfg:     run.Config{Provider: "openai", Model: "test"},
		Client:  http.DefaultClient,
		RepoMap: &m,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	got := sess.goalAckDirective()
	for _, want := range []string{"default verification surface", "just check", "go test ./..."} {
		if !strings.Contains(got, want) {
			t.Fatalf("directive missing %q:\n%s", want, got)
		}
	}
}

func TestPassLedgerNoteWrites(t *testing.T) {
	l := passLedger{goalText: "g"}

	// A pass that changes nothing extends the no-write run.
	l.noteWrites(passOutcome{})
	if l.writes != 0 || l.passesSinceWrite != 1 || l.stalledOnWrites() {
		t.Fatalf("after one read-only pass: writes=%d sinceWrite=%d stalled=%v", l.writes, l.passesSinceWrite, l.stalledOnWrites())
	}
	l.noteWrites(passOutcome{})
	if !l.stalledOnWrites() {
		t.Fatalf("two read-only passes should stall on writes, sinceWrite=%d", l.passesSinceWrite)
	}

	// A pass that changes a file resets it and records the path once.
	l.noteWrites(passOutcome{mutations: 2, mutatedPaths: []string{"a.go", "a.go", "b.go"}})
	if l.writes != 2 {
		t.Fatalf("writes = %d, want 2", l.writes)
	}
	if l.passesSinceWrite != 0 || l.stalledOnWrites() {
		t.Fatalf("a mutating pass must clear the no-write run, sinceWrite=%d", l.passesSinceWrite)
	}
	if len(l.touched) != 2 || l.touched[0] != "a.go" || l.touched[1] != "b.go" {
		t.Fatalf("touched = %v, want deduplicated [a.go b.go]", l.touched)
	}
}

// The no-write escalation outranks the periodic verification pass: a loop
// that has not written is behind on writing, and another read-only pass is
// the last thing it needs.
func TestPassLedgerPartialDirectiveTurnPrefersNoWriteEscalation(t *testing.T) {
	l := passLedger{goalText: "g", hasList: true, partialStreak: 2, passesSinceWrite: goalNoWritePasses}
	l.list = todos.New("g", []string{"alpha", "beta"})
	l.list.Items[0].Status = todos.StatusDone

	body, arm := l.partialDirectiveTurn()
	if arm {
		t.Fatal("verification must not arm while the loop is behind on writes")
	}
	if body != l.noWriteDirective() {
		t.Fatalf("body = %q, want the no-write directive", body)
	}
	if !strings.Contains(body, "beta") {
		t.Fatalf("the no-write directive should name the next step, got:\n%s", body)
	}
}

// GOAL_COMPLETE before the verification gate is downgraded either way, but a
// goal that has changed no file is asked for the edit rather than for a
// read-only re-check of a repository it never touched.
func TestPassLedgerGateDirective(t *testing.T) {
	l := passLedger{goalText: "g", hasList: true}
	l.list = todos.New("g", []string{"alpha"})
	if got := l.gateDirective(); got != l.noWriteDirective() {
		t.Fatalf("gate directive with no writes = %q, want the no-write directive", got)
	}
	l.writes = 1
	if got := l.gateDirective(); got != verificationDirective {
		t.Fatalf("gate directive after a write = %q, want verificationDirective", got)
	}
}

// The harness facts block is what lets the evaluator judge progress from
// observation rather than from the model's prose. It carries counts and
// paths only.
func TestPassLedgerGoalFacts(t *testing.T) {
	l := passLedger{goalText: "g", passes: 3, passWrites: 1, writes: 4, passWithheld: 2, touched: []string{"internal/a.go"}}
	got := l.goalFacts()
	for _, want := range []string{"Pass: 3", "Tool results withheld this pass: 2", "Files changed this pass: 1", "Files changed so far in this goal: 4", "internal/a.go"} {
		if !strings.Contains(got, want) {
			t.Fatalf("goal facts missing %q:\n%s", want, got)
		}
	}
	empty := passLedger{goalText: "g", passes: 1}
	if !strings.Contains(empty.goalFacts(), "Paths changed: none") {
		t.Fatalf("a pass with no changes should say so:\n%s", empty.goalFacts())
	}
}

// A withheld pass must not be told to stop investigating and edit: its tools
// are failing, and the repair directive is the honest escalation.
func TestPassLedgerNoWriteOrRepairDirective(t *testing.T) {
	l := passLedger{goalText: "g", writes: 0, passesSinceWrite: goalNoWritePasses, passWithheld: 0}
	if got := l.noWriteOrRepairDirective(); got != l.noWriteDirective() {
		t.Fatalf("no-write directive with no withheld pass = %q, want the ordinary no-write directive", got)
	}
	l.passWithheld = 2
	if got := l.noWriteOrRepairDirective(); got != withheldRepairDirective {
		t.Fatalf("withheld pass got %q, want the repair directive", got)
	}
	if strings.Contains(l.noWriteOrRepairDirective(), "Stop investigating") {
		t.Fatal("the repair directive must not tell a broken-tool model to stop investigating")
	}
}

func TestPassLedgerEveryPassWithheld(t *testing.T) {
	l := passLedger{goalText: "g", passes: 2, withheldPasses: 2}
	if !l.everyPassWithheld() {
		t.Fatal("two passes both withheld should be everyPassWithheld")
	}
	l.passes = 3
	if l.everyPassWithheld() {
		t.Fatal("three passes with only two withheld must not be everyPassWithheld")
	}
	l.withheldPasses = 3
	l.passes++ // a fourth, clean pass
	l.noteWithheld(passOutcome{withheld: 0})
	if l.everyPassWithheld() {
		t.Fatal("a clean pass must reset the all-withheld claim")
	}
}

// The first-pass directive is the one chance to set the mode's contract. It
// must lead with the work, not with a plan document — but it must not demand
// an edit before the model has read what it is changing.
func TestGoalAckDirectiveLeadsWithTheWork(t *testing.T) {
	for _, want := range []string{"Start the work in this pass", "update_plan", "same response as your first actions", "exact bytes you read"} {
		if !strings.Contains(goalAckDirective, want) {
			t.Fatalf("goal acknowledgement directive missing %q:\n%s", want, goalAckDirective)
		}
	}
	if strings.Contains(goalAckDirective, "'Plan:' header") {
		t.Fatal("the goal directive must not ask for a plan document before the work")
	}
	if strings.Contains(goalAckDirective, "Mutate at least one file") {
		t.Fatal("the first pass must not be forced to mutate before it has read")
	}
}

// The loop must not spend a second full model turn per pass on a progress
// report: one pass is one main-model call per iteration plus one evaluator
// call at the boundary, and nothing else.
func TestGoalPassLoopMakesNoProgressReportCall(t *testing.T) {
	srv, _, _ := goalPassServer(t, goalPassOpts{eval: []string{"GOAL_COMPLETE", "GOAL_COMPLETE"}})
	defer srv.Close()
	sess := newGoalPassSession(t, srv, true, 1)

	var mainCalls int
	res, err := sess.run(context.Background(), nil, TurnInput{Prompt: "ship the thing"}, false, func(e Event) {
		if e.Kind == EventToolStartKind {
			mainCalls++
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Passes != 2 {
		t.Fatalf("Passes = %d, want 2", res.Passes)
	}
	// One iteration per pass (MaxIterations is 1), so one tool call per pass
	// and no extra turn in between.
	if mainCalls != 2 {
		t.Fatalf("tool calls = %d, want 2 (one per pass, no progress-report turn)", mainCalls)
	}
}

// Proactive compaction must not switch itself off for a model the built-in
// registry has never heard of. That is what happened to every custom
// provider catalogue entry: modelinfo.Resolve returned ok=false and the
// boundary silently skipped compaction until a request overflowed.
func TestCompactWindowFallsBackBeyondTheBuiltinRegistry(t *testing.T) {
	newSess := func(model string, settings config.Settings) *Session {
		t.Helper()
		sess, err := NewSession(Options{
			Cfg:      run.Config{Provider: "cloudflare-ai-gateway", Model: model},
			Client:   http.DefaultClient,
			Settings: settings,
		})
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		return sess
	}

	// Unknown everywhere: the conservative default, never zero.
	if got := newSess("@cf/example/unlisted", config.Settings{}).compactWindow(); got != defaultCompactWindow {
		t.Fatalf("unknown model window = %d, want %d", got, defaultCompactWindow)
	}
	// Known to the provider catalogue but not to the built-in registry.
	cat := config.Settings{Providers: map[string]config.ProviderProfile{
		"cloudflare-ai-gateway": {Models: []config.ProviderModel{{ID: "@cf/example/big", ContextWindow: 262_144}}},
	}}
	if got := newSess("@cf/example/big", cat).compactWindow(); got != 262_144 {
		t.Fatalf("catalogue window = %d, want 262144", got)
	}
	// An explicit user override still wins.
	over := config.Settings{ContextWindows: map[string]int{"@cf/example/big": 99_000}, Providers: cat.Providers}
	if got := newSess("@cf/example/big", over).compactWindow(); got != 99_000 {
		t.Fatalf("override window = %d, want 99000", got)
	}
}

// The changed-path list is harness fact, so it is deduplicated and bounded:
// a large refactor must not turn the evaluator's evidence into a file
// listing.
func TestPassOutcomeNoteMutation(t *testing.T) {
	var o passOutcome
	o.noteMutation(callEffect{})
	if o.mutations != 0 || len(o.mutatedPaths) != 0 {
		t.Fatalf("a call that changed nothing must not count: %+v", o)
	}
	o.noteMutation(callEffect{changed: true, paths: []string{"a.go", "a.go"}})
	o.noteMutation(callEffect{changed: true, paths: []string{"a.go", "b.go"}})
	if o.mutations != 2 {
		t.Fatalf("mutations = %d, want 2", o.mutations)
	}
	if len(o.mutatedPaths) != 2 {
		t.Fatalf("mutatedPaths = %v, want the paths deduplicated", o.mutatedPaths)
	}

	var big passOutcome
	for i := 0; i < maxMutatedPaths*2; i++ {
		big.noteMutation(callEffect{changed: true, paths: []string{fmt.Sprintf("f%d.go", i)}})
	}
	if len(big.mutatedPaths) != maxMutatedPaths {
		t.Fatalf("mutatedPaths = %d, want the list bounded at %d", len(big.mutatedPaths), maxMutatedPaths)
	}
}

// The continuation directive names the step the loop expects to be worked on,
// so "continue" is never an instruction to decide what to do next.
func TestPassLedgerPartialDirectiveNamesTheNextStep(t *testing.T) {
	l := passLedger{goalText: "g", hasList: true}
	l.list = todos.New("g", []string{"alpha", "beta"})

	if got := l.partialDirective(); !strings.Contains(got, "The next action is: alpha") {
		t.Fatalf("with everything pending the first step is next, got:\n%s", got)
	}
	l.list.Items[0].Status = todos.StatusDone
	l.list.Items[1].Status = todos.StatusActive
	if got := l.partialDirective(); !strings.Contains(got, "The next action is: beta") {
		t.Fatalf("an in-progress step outranks a pending one, got:\n%s", got)
	}
	if got := l.nextStep(); got != "beta" {
		t.Fatalf("nextStep = %q, want beta", got)
	}

	none := passLedger{goalText: "g"}
	if got := none.partialDirective(); !strings.Contains(got, "update_plan") {
		t.Fatalf("with no list the directive should ask for update_plan, got:\n%s", got)
	}
}

// End to end through the loop: a pass that writes must reach the evaluator as
// a harness-observed fact. This is the whole chain — filediff snapshot,
// passOutcome, ledger, evaluator payload — and it is what stops a model
// talking its way through a goal.
func TestGoalPassLoopReportsObservedWritesToTheEvaluator(t *testing.T) {
	var mu sync.Mutex
	var evalUsers []string
	evalIdx := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
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
			writeChatJSON(w, "GOAL")
		case strings.Contains(system, "goal-progress evaluator"):
			mu.Lock()
			evalUsers = append(evalUsers, user)
			i := evalIdx
			evalIdx++
			mu.Unlock()
			if i == 0 {
				writeChatJSON(w, "GOAL_COMPLETE")
				return
			}
			writeChatJSON(w, "GOAL_COMPLETE")
		default:
			writeToolCallWithContentJSON(w, "Write", `{"path":"f.txt","content":"changed\n"}`, "Plan:\n1. Ship it\n")
		}
	}))
	defer srv.Close()

	sess := newGoalPassSession(t, srv, true, 1)
	if _, err := sess.Run(context.Background(), "ship the thing"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(evalUsers) == 0 {
		t.Fatal("the evaluator was never called")
	}
	first := evalUsers[0]
	if !strings.Contains(first, "Harness-observed facts:") {
		t.Fatalf("evaluator payload carries no facts block:\n%s", first)
	}
	if !strings.Contains(first, "Files changed this pass: 1") {
		t.Fatalf("the observed write is missing from the facts:\n%s", first)
	}
	if !strings.Contains(first, "f.txt") {
		t.Fatalf("the changed path is missing from the facts:\n%s", first)
	}
}

// The forced survey runs at most once per goal. A second one would buy more
// reading, which is never what a not-started goal is short of.
func TestGoalPassLoopSurveysAtMostOncePerGoal(t *testing.T) {
	srv, _, _ := goalPassServer(t, goalPassOpts{main: "read", eval: []string{"GOAL_NOT_STARTED"}})
	defer srv.Close()

	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o600)
	sess, err := NewSession(Options{
		Cfg:           run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "test-key", Model: "test"},
		Client:        srv.Client(),
		Workdir:       root,
		Registry:      tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024}),
		Posture:       posture.Defaults(),
		AllowExplore:  true,
		AllowPassLoop: true,
		MaxIterations: 1,
		Settings:      config.Settings{Resilience: &config.ResilienceSettings{MaxPasses: 4}},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	var surveyed int
	_, err = sess.run(context.Background(), nil, TurnInput{Prompt: "ship the thing"}, false, func(e Event) {
		if e.Kind == EventPassKind && e.Explored {
			surveyed++
		}
	})
	if err == nil || !strings.Contains(err.Error(), "max passes (4) reached") {
		t.Fatalf("expected the ceiling to stop this loop, got %v", err)
	}
	if surveyed != 1 {
		t.Fatalf("forced surveys = %d, want exactly 1 across four GOAL_NOT_STARTED verdicts", surveyed)
	}
}

// A clean verdict clears the malformed streak wherever it arrives. Both the
// exhausted-pass path and the natural-exit path go through this helper, which
// is what keeps the two in step: the natural-exit branch used to count
// malformed replies without ever resetting them.
func TestEvaluateGoalPassStreakAndGracefulStop(t *testing.T) {
	var reply string
	pipe := &rolemanager.Pipeline{Classifier: rolemanager.ClassifierFunc(
		func(_ context.Context, _ rolemanager.ClassifierPayload) (string, error) {
			return reply, nil
		})}
	sess := &Session{}
	l := passLedger{goalText: "g"}

	// One malformed verdict (re-asked once inside EvaluateGoal) counts but
	// does not stop the loop.
	reply = "not a sentinel"
	got, stop, err := sess.evaluateGoalPass(context.Background(), pipe, &l, "evidence", func(Event) {})
	if err != nil || stop {
		t.Fatalf("one malformed reply must not stop the loop: stop=%v err=%v", stop, err)
	}
	if got != rolemanager.GoalPartial {
		t.Fatalf("verdict = %q, want the fail-closed %q", got, rolemanager.GoalPartial)
	}
	if l.malformedStreak != 1 {
		t.Fatalf("malformedStreak = %d, want 1", l.malformedStreak)
	}

	// A clean verdict resets it.
	reply = string(rolemanager.GoalPartial)
	if _, stop, err = sess.evaluateGoalPass(context.Background(), pipe, &l, "evidence", func(Event) {}); err != nil || stop {
		t.Fatalf("a clean verdict must not stop the loop: stop=%v err=%v", stop, err)
	}
	if l.malformedStreak != 0 {
		t.Fatalf("malformedStreak = %d, want a clean verdict to reset it", l.malformedStreak)
	}

	// Two malformed verdicts in a row stop the loop gracefully, with a
	// warning and no error.
	reply = "still not a sentinel"
	var warned bool
	for i := 0; i < maxMalformedEvals; i++ {
		_, stop, err = sess.evaluateGoalPass(context.Background(), pipe, &l, "evidence", func(e Event) {
			if e.Kind == EventWarningKind && strings.Contains(e.Warning, "malformed") {
				warned = true
			}
		})
		if err != nil {
			t.Fatalf("a malformed reply is not a transport error: %v", err)
		}
	}
	if !stop {
		t.Fatalf("%d malformed replies in a row must stop the loop", maxMalformedEvals)
	}
	if !warned {
		t.Fatal("the graceful stop must warn the user")
	}
}

// A transport failure leaves the verdict unknown, so the loop fails closed to
// GOAL_PARTIAL rather than throwing the run away. Consecutive failures are
// counted, and the streak stops the loop gracefully with the work so far — a
// permanently unreachable evaluator must not grant unbounded passes.
func TestEvaluateGoalPassTransportFailureFailsClosedThenStops(t *testing.T) {
	pipe := &rolemanager.Pipeline{Classifier: rolemanager.ClassifierFunc(
		func(_ context.Context, _ rolemanager.ClassifierPayload) (string, error) {
			return "", errors.New("provider returned 401: no cookie auth credentials found")
		})}
	sess := &Session{}
	l := passLedger{goalText: "g"}

	got, stop, err := sess.evaluateGoalPass(context.Background(), pipe, &l, "evidence", func(Event) {})
	if err != nil {
		t.Fatalf("one transport failure must not abort the run: %v", err)
	}
	if stop {
		t.Fatal("one transport failure must not stop the loop")
	}
	if got != rolemanager.GoalPartial {
		t.Fatalf("verdict = %q, want the fail-closed %q", got, rolemanager.GoalPartial)
	}
	if l.evalErrorStreak != 1 {
		t.Fatalf("evalErrorStreak = %d, want 1", l.evalErrorStreak)
	}

	var warned bool
	_, stop, err = sess.evaluateGoalPass(context.Background(), pipe, &l, "evidence", func(e Event) {
		if e.Kind == EventWarningKind && strings.Contains(e.Warning, "failed 2 times in a row") {
			warned = true
		}
	})
	if err != nil {
		t.Fatalf("a transport failure is not a terminal error: %v", err)
	}
	if !stop {
		t.Fatalf("%d consecutive transport failures must stop the loop", maxGoalEvalErrors)
	}
	if !warned {
		t.Fatal("the graceful stop must warn the user")
	}
}

// A clean evaluator contact (even a malformed reply) resets the transport
// failure streak, because the evaluator has been reached again.
func TestEvaluateGoalPassTransportFailureStreakResets(t *testing.T) {
	reply := ""
	pipe := &rolemanager.Pipeline{Classifier: rolemanager.ClassifierFunc(
		func(_ context.Context, _ rolemanager.ClassifierPayload) (string, error) {
			if reply == "" {
				return "", errors.New("provider returned 500: upstream down")
			}
			return reply, nil
		})}
	sess := &Session{}
	l := passLedger{goalText: "g"}

	_, stop, err := sess.evaluateGoalPass(context.Background(), pipe, &l, "evidence", func(Event) {})
	if err != nil || stop || l.evalErrorStreak != 1 {
		t.Fatalf("first transport failure: stop=%v err=%v streak=%d", stop, err, l.evalErrorStreak)
	}

	// A malformed reply still reaches the evaluator, so it resets the streak.
	reply = "not a sentinel"
	_, stop, err = sess.evaluateGoalPass(context.Background(), pipe, &l, "evidence", func(Event) {})
	if err != nil || stop {
		t.Fatalf("malformed reply after transport failure: stop=%v err=%v", stop, err)
	}
	if l.evalErrorStreak != 0 {
		t.Fatalf("evalErrorStreak = %d, want a reached evaluator to reset it", l.evalErrorStreak)
	}
	if l.malformedStreak != 1 {
		t.Fatalf("malformedStreak = %d, want 1", l.malformedStreak)
	}
}
