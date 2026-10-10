package rc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sessionctl"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/turnlog"
	"github.com/vulnetix/belai/internal/webask"
)

// planRunner answers its first turn with a recorded plan file, as a plan-mode
// turn does, and keeps every turn input it was given.
type planRunner struct {
	mu     sync.Mutex
	path   string
	inputs []agent.TurnInput
}

func (p *planRunner) RunInputObserved(_ context.Context, _ []run.Turn, in agent.TurnInput, emit func(agent.Event)) (run.Result, error) {
	p.mu.Lock()
	p.inputs = append(p.inputs, in)
	first := len(p.inputs) == 1
	p.mu.Unlock()
	if first || in.Directive != "" {
		emit(agent.Event{Kind: agent.EventPlanFileKind, PlanName: "plan-20260101-000000-demo", PlanPath: p.path})
	}
	return run.Result{Reply: "plan: " + in.Prompt, PlanPath: p.path}, nil
}

func (p *planRunner) turns() []agent.TurnInput {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]agent.TurnInput(nil), p.inputs...)
}

// planHarness runs a remote session whose first turn writes a plan.
type planHarness struct {
	t       *testing.T
	runner  *planRunner
	log     *turnlog.Log
	answers *answerMirror
	ctl     *Controller
	cancel  context.CancelFunc
	done    chan struct{}
}

func startPlanSession(t *testing.T, withAsks bool) *planHarness {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "plan-20260101-000000-demo.md")
	body := "# Plan: demo\n\n## Summary\n\nDo it.\n\n## Key Changes\n\n1. Change a\n   - Files: a.go\n   - Verify: go test ./...\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &planRunner{path: path}
	st := testState()
	st.Mode = "plan"
	st.Ask = withAsks
	ctl, err := NewController(st, sessionctl.Env{}, func(sessionctl.State) (Runner, error) { return r, nil })
	if err != nil {
		t.Fatal(err)
	}
	h := &planHarness{
		t: t, runner: r, log: testLog(t), ctl: ctl,
		answers: &answerMirror{answers: make(chan sessionsync.RemoteAnswer, 8)},
		done:    make(chan struct{}),
	}
	m := &fakeMirror{prompts: make(chan sessionsync.RemotePrompt, 4)}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() {
		defer close(h.done)
		_ = RunSession(ctx, SessionOptions{
			Log: h.log, Mirror: m, Prompt: "plan the demo", Idle: time.Hour, AskWait: time.Hour,
			Controls: ctl, Commands: make(chan sessionsync.RemoteCommand), AckCommand: (&cmdAcks{}).ack,
			Answers: h.answers,
		})
	}()
	t.Cleanup(func() { cancel(); <-h.done })
	return h
}

func (h *planHarness) entries(typ string) []map[string]any {
	es, _ := h.log.Writer().Entries()
	var out []map[string]any
	for _, e := range es {
		if e.Type == typ {
			out = append(out, e.Meta)
		}
	}
	return out
}

func (h *planHarness) answer(id, askID, payload string) {
	h.answers.answers <- sessionsync.RemoteAnswer{ID: id, AskID: askID, Kind: webask.KindPlanReview, Payload: json.RawMessage(payload)}
}

func (h *planHarness) acks() string {
	h.answers.mu.Lock()
	defer h.answers.mu.Unlock()
	return strings.Join(h.answers.acks, "|")
}

// A plan turn ends with the plan offered to the web: the ask carries the plan
// text and the choices a remote session can honour.
func TestPlanTurnOffersTheReviewToTheWeb(t *testing.T) {
	h := startPlanSession(t, true)
	askID := openAsk(t, h.log, 1)
	asks := h.entries("ask")
	if len(asks) != 1 || asks[0]["kind"] != webask.KindPlanReview {
		t.Fatalf("asks = %v", asks)
	}
	if !strings.Contains(asks[0]["plan"].(string), "## Key Changes") {
		t.Fatalf("the ask must carry the plan text: %v", asks[0])
	}
	if asks[0]["plan_name"] != "plan-20260101-000000-demo" {
		t.Fatalf("plan_name = %v", asks[0]["plan_name"])
	}
	opts, _ := json.Marshal(asks[0]["options"])
	if string(opts) != `["approve_here","refine","stay"]` {
		t.Fatalf("options = %s: a remote session cannot fork, so no approve_new", opts)
	}
	if askID == "" {
		t.Fatal("no ask id")
	}
}

func TestApprovingThePlanExecutesItOnTheFullSurface(t *testing.T) {
	h := startPlanSession(t, true)
	askID := openAsk(t, h.log, 1)
	h.answer("a1", askID, `{"choice":"approve_here"}`)
	eventuallyRC(t, "the execute turn", func() bool { return len(h.runner.turns()) == 2 })
	exec := h.runner.turns()[1]
	if !exec.ExecutePlan || exec.PlanName != "plan-20260101-000000-demo" || exec.ForceMode != modes.ModeAgent || exec.Prompt != executePlanPrompt {
		t.Fatalf("execute turn = %+v", exec)
	}
	if h.ctl.Mode() != modes.ModeAgent {
		t.Fatalf("session mode = %q, want agent after approval", h.ctl.Mode())
	}
	if !strings.Contains(h.acks(), "a1:accepted") {
		t.Fatalf("acks = %s", h.acks())
	}
	if ans := h.entries("ask_answer"); len(ans) != 1 || ans[0]["plan_choice"] != webask.PlanApproveHere || ans[0]["source"] != webask.FromWeb {
		t.Fatalf("ask_answer = %v", ans)
	}
}

func TestRefiningThePlanSendsTheNotesBackToThePlanner(t *testing.T) {
	h := startPlanSession(t, true)
	askID := openAsk(t, h.log, 1)
	h.answer("a1", askID, `{"choice":"refine","notes":"also cover the README"}`)
	eventuallyRC(t, "the refine turn", func() bool { return len(h.runner.turns()) == 2 })
	ref := h.runner.turns()[1]
	if ref.ExecutePlan || ref.ForceMode != modes.ModePlan || ref.Directive != planRefineDirective || ref.Prompt != "also cover the README" {
		t.Fatalf("refine turn = %+v", ref)
	}
	// The refined plan is offered again, as a new review.
	openAsk(t, h.log, 2)
}

func TestLeavingThePlanRunsNothing(t *testing.T) {
	h := startPlanSession(t, true)
	askID := openAsk(t, h.log, 1)
	h.answer("a1", askID, `{"choice":"stay"}`)
	eventuallyRC(t, "the answer to be acked", func() bool { return strings.Contains(h.acks(), "a1:accepted") })
	time.Sleep(100 * time.Millisecond)
	if n := len(h.runner.turns()); n != 1 {
		t.Fatalf("turns = %d, want only the plan turn", n)
	}
}

func TestPlanReviewRefusesWhatDoesNotFit(t *testing.T) {
	h := startPlanSession(t, true)
	askID := openAsk(t, h.log, 1)
	h.answer("r1", "some-other-ask", `{"choice":"approve_here"}`)
	h.answer("r2", askID, `{"choice":"approve_new"}`)
	h.answer("r3", askID, `{"choice":"refine","notes":"   "}`)
	h.answer("r4", askID, `{"choice":"delete_everything"}`)
	h.answers.answers <- sessionsync.RemoteAnswer{ID: "r5", AskID: askID, Kind: webask.KindPermission, Payload: json.RawMessage(`{"decision":"allow_once"}`)}
	eventuallyRC(t, "every answer to be acked", func() bool { return strings.Count(h.acks(), ":refused") == 5 })
	if n := len(h.runner.turns()); n != 1 {
		t.Fatalf("a refused answer started a turn: %d turns", n)
	}
	// The review is still open: a valid answer after the refusals is taken.
	h.answer("ok", askID, `{"choice":"stay"}`)
	eventuallyRC(t, "the valid answer", func() bool { return strings.Contains(h.acks(), "ok:accepted") })
}

// Without asks the web cannot answer, so nothing is offered; the transcript
// still says where the plan is.
func TestPlanTurnWithoutAsksRecordsThePath(t *testing.T) {
	h := startPlanSession(t, false)
	eventuallyRC(t, "the plan path line", func() bool {
		es, _ := h.log.Writer().Entries()
		for _, e := range es {
			if e.Type == "system" && strings.HasPrefix(e.Content, "plan written: ") {
				return true
			}
		}
		return false
	})
	if asks := h.entries("ask"); len(asks) != 0 {
		t.Fatalf("an unanswerable plan review was offered: %v", asks)
	}
}

// A plain remote session (no web controls, so no permission asks) is the shape
// of the failing session d9292a3d. It still offers the plan for review, takes
// the answer, and runs an approved plan on the executor's session, because its
// own agent has asks off and no terminal to approve each edit on.
func TestPlainRemoteSessionOffersAndExecutesThePlan(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plan-20260101-000000-demo.md")
	if err := os.WriteFile(path, []byte("# Plan: demo\n\n## Summary\n\nx\n\n## Key Changes\n\n1. A\n   - Files: a.go\n   - Verify: go build\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	planner := &planRunner{path: path}
	executor := &planRunner{path: path}
	log := testLog(t)
	answers := &answerMirror{answers: make(chan sessionsync.RemoteAnswer, 4)}
	m := &fakeMirror{prompts: make(chan sessionsync.RemotePrompt, 4)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = RunSession(ctx, SessionOptions{
			Agent: planner, Log: log, Mirror: m, Prompt: "plan the demo", Mode: modes.ModePlan, Idle: time.Hour,
			Answers:  answers,
			Executor: func() (Runner, error) { return executor, nil },
		})
	}()
	t.Cleanup(func() { cancel(); <-done })

	askID := openAsk(t, log, 1)
	es, _ := log.Writer().Entries()
	var kind any
	for _, e := range es {
		if e.ID == askID {
			kind = e.Meta["kind"]
		}
	}
	if kind != webask.KindPlanReview {
		t.Fatalf("a plain session must offer the plan: ask kind = %v", kind)
	}
	answers.answers <- sessionsync.RemoteAnswer{ID: "a1", AskID: askID, Kind: webask.KindPlanReview, Payload: json.RawMessage(`{"choice":"approve_here"}`)}
	eventuallyRC(t, "the execute turn on the executor", func() bool { return len(executor.turns()) == 1 })
	exec := executor.turns()[0]
	if !exec.ExecutePlan || exec.PlanName != "plan-20260101-000000-demo" || exec.ForceMode != modes.ModeAgent {
		t.Fatalf("execute turn = %+v", exec)
	}
	if n := len(planner.turns()); n != 1 {
		t.Fatalf("the planning session ran %d turns, want only the plan turn", n)
	}
}
