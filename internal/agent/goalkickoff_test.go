package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/clarify"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/explore"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/repomap"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
)

// kickoffServer scripts a goal kickoff: the clarifier asks one question, each
// research subagent answers with a report naming its task, the main model
// writes a file every round, and the evaluator says GOAL_COMPLETE. It records
// the main model's last user messages and the evaluator's inputs.
type kickoffServer struct {
	mu       sync.Mutex
	mainLast []string // every user message of each main-model request
	mainMsgs []int    // the message count of each main-model request
	evalNext string   // evaluator verdict (default GOAL_COMPLETE)
	sawTask  bool     // a main request advertised Task
	// replyAfterWrite: every other main call ends the pass with a plain
	// reply instead of a write (a natural exit).
	replyAfterWrite bool
	// researchDelay holds each research reply back.
	researchDelay time.Duration
	evals         []string
	writes        int
}

func (k *kickoffServer) handler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	// Content is a string or, once a block is sealed into it, a list of
	// parts; the raw JSON is enough to look for text in.
	text := func(raw json.RawMessage) string {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
		return string(raw)
	}
	var system, firstUser, lastUser, allUsers string
	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			system = text(m.Content)
		case "user":
			if firstUser == "" {
				firstUser = text(m.Content)
			}
			lastUser = text(m.Content)
			allUsers += lastUser + "\n"
		}
	}
	if len(req.Messages) == 0 {
		writeChatJSON(w, "")
		return
	}
	switch {
	case strings.Contains(system, "security classifier"):
		writeChatJSON(w, "SAFE")
	case strings.Contains(system, "operating-mode classifier"):
		writeChatJSON(w, "GOAL")
	case strings.Contains(system, "clarification questionnaire"):
		writeChatJSON(w, `{"groups":[{"context":"Which output format?","options":[{"label":"Plain text"},{"label":"JSON"}]}]}`)
	case strings.Contains(system, "goal-progress evaluator"):
		k.mu.Lock()
		k.evals = append(k.evals, lastUser)
		verdict := k.evalNext
		k.mu.Unlock()
		if verdict == "" {
			verdict = "GOAL_COMPLETE"
		}
		writeChatJSON(w, verdict)
	case strings.Contains(system, "goal-contract writer"):
		writeChatJSON(w, "unusable")
	case strings.Contains(firstUser, "Goal: ship the thing"):
		// A research subagent: one report, no tools.
		task := "research"
		for _, name := range []string{"Name the exact files", "Name the existing tests", "agent instruction files", "Name the conventions"} {
			if strings.Contains(firstUser, name) {
				task = name
			}
		}
		// Research is slower than the kickoff pass, as it is in practice, so
		// the pass starts before the reports land.
		time.Sleep(k.researchDelay)
		writeChatJSON(w, "REPORT from "+task)
	case isReportTurn(lastUser):
		writeChatJSON(w, "Final report: shipped.")
	default:
		k.mu.Lock()
		k.mainLast = append(k.mainLast, allUsers)
		k.mainMsgs = append(k.mainMsgs, len(req.Messages))
		for _, tool := range req.Tools {
			if tool.Function.Name == "Task" {
				k.sawTask = true
			}
		}
		k.writes++
		n := k.writes
		reply := k.replyAfterWrite && n%2 == 0
		k.mu.Unlock()
		if reply {
			writeChatJSON(w, "Done: the thing is shipped.")
			return
		}
		args := fmt.Sprintf(`{"path":"f.txt","content":%q}`, fmt.Sprintf("pass %d\n", n))
		writeToolCallWithContentJSON(w, "Write", args, "Plan:\n1. Ship\n[DONE:1]\n")
	}
}

func newKickoffSession(t *testing.T, srv *httptest.Server, repoMap *repomap.Map) *Session {
	t.Helper()
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o600)
	sess, err := NewSession(Options{
		Cfg:           run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "test-key", Model: "test"},
		Client:        srv.Client(),
		Workdir:       root,
		Registry:      tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024}, &tools.Write{Root: root, MaxBytes: tools.MaxWriteBytes, Cwd: tools.NewCwd(root)}, tools.Todo{}),
		Posture:       posture.Defaults(),
		AskDisabled:   true,
		AllowExplore:  true,
		AllowClarify:  true,
		AllowPassLoop: true,
		MaxIterations: 2,
		RepoMap:       repoMap,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return sess
}

// The kickoff asks the user first, starts the work on the answers while the
// research runs, delivers the research with the clarify-again directive, and
// judges the kickoff pass together with the first official turn.
func TestGoalKickoffAsksResearchesAndJudgesTogether(t *testing.T) {
	k := &kickoffServer{researchDelay: 300 * time.Millisecond}
	srv := httptest.NewServer(http.HandlerFunc(k.handler))
	defer srv.Close()
	sess := newKickoffSession(t, srv, &repomap.Map{AgentsFiles: []repomap.AgentsFile{{Name: "AGENTS.md"}}})

	var asked []string
	var delivered bool
	res, err := sess.run(context.Background(), nil, TurnInput{Prompt: "ship the thing"}, false, func(e Event) {
		switch {
		case e.Kind == EventClarifyAskKind && e.Reply != nil:
			asked = append(asked, e.Clarify.Groups[0].Context)
			e.Reply <- clarify.Answers{Items: []clarify.Answer{{GroupIndex: 0, Chosen: []int{1}, Note: "use the JSON shape"}}}
		case e.Kind == EventWarningKind && strings.Contains(e.Warning, "goal research delivered"):
			delivered = true
		}
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(asked) != 1 || asked[0] != "Which output format?" {
		t.Fatalf("asked = %v, want the one kickoff question", asked)
	}
	if !delivered {
		t.Fatal("the research was never delivered")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.mainLast) == 0 {
		t.Fatal("the main model never ran")
	}
	first := k.mainLast[0]
	if !strings.Contains(first, "Research subagents are gathering") || strings.Contains(first, "REPORT from") {
		t.Fatalf("the kickoff pass must start before the research lands and say so; first main request:\n%s", first)
	}
	if !strings.Contains(first, "Clarifications from the user") || !strings.Contains(first, "use the JSON shape") {
		t.Fatalf("the kickoff pass starts on the user's answers; first main request:\n%s", first)
	}
	var sawResearch bool
	for _, m := range k.mainLast {
		if strings.Contains(m, "research subagents' reports are above") {
			sawResearch = true
		}
	}
	if !sawResearch {
		t.Fatal("the research-arrived directive never reached the main model")
	}
	if len(k.evals) == 0 {
		t.Fatal("the evaluator never ran")
	}
	if !strings.Contains(k.evals[0], "REPORT from") {
		t.Fatalf("the first evaluation must see the research reports with the turn:\n%s", k.evals[0])
	}
	if res.GoalSentinel != "GOAL_COMPLETE" {
		t.Fatalf("GoalSentinel = %q, want GOAL_COMPLETE", res.GoalSentinel)
	}
	if !k.sawTask {
		t.Fatal("a kicked-off goal must be able to send more subagents: Task was not advertised")
	}
}

// The answers ride on the user's prompt turn, as the user's own direction.
func TestGoalKickoffAnswersRideOnThePrompt(t *testing.T) {
	k := &kickoffServer{}
	srv := httptest.NewServer(http.HandlerFunc(k.handler))
	defer srv.Close()
	sess := newKickoffSession(t, srv, nil)

	_, err := sess.run(context.Background(), nil, TurnInput{Prompt: "ship the thing"}, false, func(e Event) {
		if e.Kind == EventClarifyAskKind && e.Reply != nil {
			e.Reply <- clarify.Answers{Items: []clarify.Answer{{GroupIndex: 0, Chosen: []int{1}}}}
		}
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	// Without a repo map no research runs, so the kickoff pass is an ordinary
	// first pass and says nothing about research.
	if strings.Contains(k.mainLast[0], "Research subagents") {
		t.Fatal("no research ran, so the kickoff pass must not promise any")
	}
}

func TestGoalKickoffSkipsWhereNobodyNeedsIt(t *testing.T) {
	s := &Session{allowPassLoop: true, allowClarify: true}
	goal := TurnInput{}
	dec := rolemanager.ModeDecision{Mode: modes.ModeGoal}
	if !s.kickoffEligible(goal, dec) {
		t.Fatal("a top-level goal that can ask is eligible")
	}
	for name, in := range map[string]TurnInput{
		"plan execution": {ExecutePlan: true},
		"harness prompt": {NoGoalDraft: true},
		"decision":       {Decision: true},
	} {
		if s.kickoffEligible(in, dec) {
			t.Errorf("%s must skip the kickoff", name)
		}
	}
	s.turnSimple = true
	if s.kickoffEligible(goal, dec) {
		t.Error("a simple request must skip the kickoff")
	}
	s.turnSimple = false
	if s.kickoffEligible(goal, rolemanager.ModeDecision{Mode: modes.ModeAgent}) {
		t.Error("agent mode has no kickoff")
	}
	sub := &Session{allowPassLoop: true, allowClarify: true, exploreSubagent: true}
	if sub.kickoffEligible(goal, dec) {
		t.Error("a subagent never kicks off")
	}
	if (&Session{allowPassLoop: true, allowExplore: true}).kickoffEligible(goal, dec) {
		t.Error("research needs the repo map it is seeded from")
	}
}

func TestPlanGoalResearchSeedsFromMemory(t *testing.T) {
	tasks := explore.PlanGoalResearch("add a flag", []string{"AGENTS.md", "CLAUDE.md"})
	if len(tasks) != 3 {
		t.Fatalf("tasks = %d, want 3", len(tasks))
	}
	if !strings.Contains(tasks[2].Prompt, "AGENTS.md, CLAUDE.md") {
		t.Fatalf("the conventions task must name the instruction files: %q", tasks[2].Prompt)
	}
	for _, task := range tasks {
		if !strings.Contains(task.Prompt, "Goal: add a flag") {
			t.Fatalf("every task carries the goal: %q", task.Prompt)
		}
	}
}

// A goal stuck on identical passes asks how to proceed. Steering resets the
// loop; a second stall that gets no steering ends it.
func TestGoalStuckAsksThenResetsThenEnds(t *testing.T) {
	srv, _, _ := goalPassServer(t, goalPassOpts{main: "read", eval: []string{}})
	defer srv.Close()
	sess := newGoalPassSession(t, srv, true, 2)
	sess.allowClarify = true

	var questions, stuck int
	res, err := sess.run(context.Background(), nil, TurnInput{Prompt: "ship the thing", NoGoalDraft: true}, false, func(e Event) {
		switch {
		case e.Kind == EventClarifyAskKind && e.Reply != nil:
			questions++
			if questions == 1 {
				e.Reply <- clarify.Answers{Items: []clarify.Answer{{GroupIndex: 0, Chosen: []int{0}, Note: "edit f.txt directly"}}}
				return
			}
			e.Reply <- clarify.Answers{Items: []clarify.Answer{{GroupIndex: 0, Chosen: []int{1}}}}
		case e.Kind == EventWarningKind && strings.Contains(e.Warning, "goal stuck"):
			stuck++
		}
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if stuck != 2 || questions != 2 {
		t.Fatalf("stuck=%d questions=%d, want two stalls each asked once", stuck, questions)
	}
	// Five identical passes, the stuck pass, a reset, five more and the
	// second stuck pass.
	if want := 2 * (goalStuckPasses + 1); res.Passes != want {
		t.Fatalf("Passes = %d, want %d", res.Passes, want)
	}
	if res.GoalSentinel != "GOAL_PARTIAL" {
		t.Fatalf("GoalSentinel = %q, want GOAL_PARTIAL", res.GoalSentinel)
	}
}

// Nobody to ask: the stuck goal ends at once with its report.
func TestGoalStuckWithNobodyToAskEnds(t *testing.T) {
	srv, _, _ := goalPassServer(t, goalPassOpts{main: "read", eval: []string{}})
	defer srv.Close()
	sess := newGoalPassSession(t, srv, true, 2)

	res, err := sess.run(context.Background(), nil, TurnInput{Prompt: "ship the thing"}, false, func(Event) {})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Passes != goalStuckPasses {
		t.Fatalf("Passes = %d, want %d", res.Passes, goalStuckPasses)
	}
}

func TestSteeredBy(t *testing.T) {
	q := stuckQuestionnaire()
	cases := []struct {
		a    clarify.Answers
		want bool
	}{
		{clarify.Answers{Items: []clarify.Answer{{GroupIndex: 0, Chosen: []int{0}}}}, true},
		{clarify.Answers{Items: []clarify.Answer{{GroupIndex: 0, Chosen: []int{1}}}}, false},
		{clarify.Answers{Items: []clarify.Answer{{GroupIndex: 0, Chosen: []int{1}, Note: "actually try X"}}}, true},
		{clarify.Answers{Items: []clarify.Answer{{GroupIndex: 0, Skipped: true}}}, false},
	}
	for i, c := range cases {
		if got := steeredBy(q, c.a); got != c.want {
			t.Errorf("case %d: steeredBy = %v, want %v", i, got, c.want)
		}
	}
	if err := q.Validate(); err != nil {
		t.Fatalf("the harness question must be a valid questionnaire: %v", err)
	}
}

// Every goal pass continues the conversation: it sees the passes before it
// and the directive the boundary appended. A := in the loop once declared a
// turns local to each iteration, so every pass restarted from the prompt and
// no boundary directive ever reached the model.
func TestGoalPassesContinueTheConversation(t *testing.T) {
	k := &kickoffServer{evalNext: "GOAL_PARTIAL"}
	srv := httptest.NewServer(http.HandlerFunc(k.handler))
	defer srv.Close()
	sess := newKickoffSession(t, srv, nil)
	sess.allowClarify = false
	sess.settings.Resilience = &config.ResilienceSettings{MaxPasses: 3}

	_, _ = sess.run(context.Background(), nil, TurnInput{Prompt: "ship the thing", NoGoalDraft: true}, false, func(Event) {})
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.mainMsgs) < 4 {
		t.Fatalf("main requests = %v, want several passes", k.mainMsgs)
	}
	for i := 1; i < len(k.mainMsgs); i++ {
		if k.mainMsgs[i] <= k.mainMsgs[i-1] {
			t.Fatalf("request %d has %d messages after %d: a pass dropped the conversation (%v)", i, k.mainMsgs[i], k.mainMsgs[i-1], k.mainMsgs)
		}
	}
	last := k.mainLast[len(k.mainLast)-1]
	if !strings.Contains(last, "Turn ") || !strings.Contains(last, "is over and the goal is not met yet") {
		t.Fatalf("the turn review never reached the model:\n%s", last)
	}
}

// Research that finished while the user was answering is in the first
// request, and the first directive does not promise research still to come.
func TestGoalKickoffResearchReadyBeforeTheAnswers(t *testing.T) {
	k := &kickoffServer{}
	srv := httptest.NewServer(http.HandlerFunc(k.handler))
	defer srv.Close()
	sess := newKickoffSession(t, srv, &repomap.Map{})

	_, err := sess.run(context.Background(), nil, TurnInput{Prompt: "ship the thing"}, false, func(e Event) {
		if e.Kind == EventClarifyAskKind && e.Reply != nil {
			time.Sleep(300 * time.Millisecond) // a user who takes a moment
			e.Reply <- clarify.Answers{Items: []clarify.Answer{{GroupIndex: 0, Chosen: []int{0}}}}
		}
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	first := k.mainLast[0]
	if !strings.Contains(first, "REPORT from") || !strings.Contains(first, "reports are above") {
		t.Fatalf("ready research rides on the first request:\n%s", first)
	}
	if strings.Contains(first, "Research subagents are gathering") {
		t.Fatal("the first directive must not promise research that is already here")
	}
}

// A pass that ends on a plain reply is judged on the whole turn, not the reply
// alone: shown only "done", the evaluator could never see the check behind it.
func TestGoalNaturalExitIsJudgedOnTheWholeTurn(t *testing.T) {
	k := &kickoffServer{replyAfterWrite: true}
	srv := httptest.NewServer(http.HandlerFunc(k.handler))
	defer srv.Close()
	sess := newKickoffSession(t, srv, nil)
	sess.allowClarify = false

	if _, err := sess.run(context.Background(), nil, TurnInput{Prompt: "ship the thing", NoGoalDraft: true}, false, func(Event) {}); err != nil {
		t.Fatalf("run: %v", err)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.evals) == 0 {
		t.Fatal("the evaluator never ran")
	}
	if !strings.Contains(k.evals[0], "Done: the thing is shipped.") || !strings.Contains(k.evals[0], "Write") {
		t.Fatalf("the natural-exit evaluation must see the turn's tool calls and the reply:\n%s", k.evals[0])
	}
}

// Steering a stuck goal joins the goal text, so the evaluator judges the goal
// as the user now wants it rather than the approach they overruled.
func TestSteerJoinsTheGoal(t *testing.T) {
	l := passLedger{goalText: "wait for the deploy to create READY"}
	l.steer("  ")
	if l.goalText != "wait for the deploy to create READY" {
		t.Fatal("an empty answer adds nothing")
	}
	l.steer("create READY yourself")
	if !strings.Contains(l.goalText, "Direction from the user") || !strings.HasSuffix(l.goalText, "create READY yourself") {
		t.Fatalf("goal = %q", l.goalText)
	}
}
