package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/goals"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/plans"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
)

func newReadOnlyAgentSession(t *testing.T, root string, srvURL string) *Session {
	t.Helper()
	cfg := run.Config{Provider: "openai", BaseURL: srvURL, APIKey: "test-key", Model: "test"}
	sess, err := NewSession(Options{
		Cfg:           cfg,
		Registry:      tools.Default(root, false),
		Posture:       posture.AllIgnore(),
		Workdir:       root,
		ReadOnlyAgent: true,
		SkipNonceSeed: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return sess
}

// TestReadOnlyAgentTurnRefusesWrite runs a real agent-mode turn with the
// read_only setting on: the Write the model asks for is refused with the
// reason named, and nothing lands on disk.
func TestReadOnlyAgentTurnRefusesWrite(t *testing.T) {
	root := t.TempDir()
	srv := mockSecurityServer("Write", `{"path":"x.txt","content":"hello"}`, "done")
	defer srv.Close()
	sess := newReadOnlyAgentSession(t, root, srv.URL)
	sess.client = srv.Client()

	var result string
	_, err := sess.run(context.Background(), nil, TurnInput{Prompt: "write a file", ForceMode: modes.ModeAgent}, false, func(e Event) {
		if e.Kind == EventToolResultKind && e.ToolName == "Write" {
			result = e.ToolResult
		}
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(result, "read_only setting is on for agent mode") {
		t.Fatalf("Write result = %q, want the read_only refusal", result)
	}
	if _, err := os.Stat(filepath.Join(root, "x.txt")); !os.IsNotExist(err) {
		t.Fatalf("x.txt must not exist, stat err = %v", err)
	}
	if sess.turnReadOnly {
		t.Fatal("the read-only latch must be restored after the turn")
	}
}

// TestReadOnlyLatchScopesToAgentMode pins which turns the read_only setting
// narrows: agent mode only. Goal mode and plan execution keep the full
// surface, including the mutating Bash.
func TestReadOnlyLatchScopesToAgentMode(t *testing.T) {
	root := t.TempDir()
	sess := newReadOnlyAgentSession(t, root, "http://127.0.0.1:0")

	sess.turnReadOnly = true
	reg, openAI, _ := sess.toolSurface()
	if _, ok := reg.Find("Write"); ok {
		t.Fatal("read-only agent surface must not offer Write")
	}
	for _, d := range openAI {
		if d.Function.Name == "Write" || d.Function.Name == "Edit" {
			t.Fatalf("read-only agent surface advertises %s", d.Function.Name)
		}
	}
	if tool, refusal := sess.execTool("Bash"); tool == nil || !tool.(*tools.Bash).ReadOnly {
		t.Fatalf("read-only agent turn must execute the allowlisted Bash, got %v %q", tool, refusal)
	}

	sess.turnReadOnly = false
	reg, _, _ = sess.toolSurface()
	if _, ok := reg.Find("Write"); !ok {
		t.Fatal("goal/plan-execution surface must offer Write even with read_only on")
	}
	if tool, _ := sess.execTool("Bash"); tool == nil || tool.(*tools.Bash).ReadOnly {
		t.Fatal("goal/plan-execution turns must execute the full Bash")
	}
}

func TestIsContinuation(t *testing.T) {
	yes := []string{
		"", "continue", "Continue goal", "continue the goal", "resume", "keep going",
		"carry on with the plan", "please continue", "proceed.",
		"Continue the goal, write the fixes and keep the docs aligned",
	}
	no := []string{
		"fix the model provider switcher",
		"continuous integration is broken, fix it",
		"test change directory",
		"continue " + strings.Repeat("x", maxContinuationLen),
	}
	for _, p := range yes {
		if !IsContinuation(p) {
			t.Errorf("IsContinuation(%q) = false, want true", p)
		}
	}
	for _, p := range no {
		if IsContinuation(p) {
			t.Errorf("IsContinuation(%q) = true, want false", p)
		}
	}
}

func TestContinuationExtra(t *testing.T) {
	cases := map[string]string{
		"continue goal":                         "",
		"Continue the goal, write the fixes":    "write the fixes",
		"keep going: and update docs/README.md": "and update docs/README.md",
	}
	for in, want := range cases {
		if got := continuationExtra(in); got != want {
			t.Errorf("continuationExtra(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestGoalContinuationResumesPriorGoal checks the carrier decision half of a
// continuation: the prior objective is carried and the prior state latched,
// so the loop keeps the same goal id and counters.
func TestGoalContinuationResumesPriorGoal(t *testing.T) {
	srv, _, _ := goalPassServer(t, goalPassOpts{eval: []string{"GOAL_COMPLETE", "GOAL_COMPLETE"}})
	defer srv.Close()
	sess := newGoalPassSession(t, srv, true, 2)

	prior := goalStateFixture("Objective:\nfix the switcher", 3, 1200)
	var states []string
	var tokens []int
	_, runErr := sess.run(context.Background(), nil, TurnInput{Prompt: "continue goal", ForceMode: modes.ModeGoal, PriorGoal: &prior}, false, func(e Event) {
		if e.Kind == EventGoalStateKind && e.GoalState != nil {
			states = append(states, e.GoalState.ID)
			tokens = append(tokens, e.GoalState.TokensUsed)
			if !strings.HasPrefix(e.GoalState.Objective, "Objective:\nfix the switcher") {
				t.Fatalf("objective = %q, want the prior objective", e.GoalState.Objective)
			}
		}
	})
	if len(states) == 0 {
		t.Fatalf("no goal state emitted (run err: %v)", runErr)
	}
	for _, id := range states {
		if id != prior.ID {
			t.Fatalf("goal id = %q, want the prior id %q", id, prior.ID)
		}
	}
	if tokens[len(tokens)-1] < prior.TokensUsed {
		t.Fatalf("tokens went backwards: %v (prior %d)", tokens, prior.TokensUsed)
	}
}

func goalStateFixture(objective string, passes, tokens int) goals.GoalState {
	gs := goals.NewGoalState(objective)
	gs.Passes = passes
	gs.TokensUsed = tokens
	gs.TimeUsedSeconds = 30
	return gs
}

// TestExecutePlanRunsGoalLoopOnFullSurface is the approved-plan contract: the
// execute turn runs the goal pass loop (goal state is emitted), writes land
// even with read_only on, and an engaged agent profile does not narrow it.
func TestExecutePlanRunsGoalLoopOnFullSurface(t *testing.T) {
	srv, _, _ := goalPassServer(t, goalPassOpts{mode: "AGENT", eval: []string{"GOAL_COMPLETE", "GOAL_COMPLETE"}})
	defer srv.Close()

	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o600)
	if _, err := plans.Save(root, plans.Plan{Name: "ship", Content: "1. Write f.txt\n2. Verify"}); err != nil {
		t.Fatalf("save plan: %v", err)
	}
	cwd := tools.NewCwd(root)
	cfg := run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "test-key", Model: "test"}
	sess, err := NewSession(Options{
		Cfg:           cfg,
		Client:        srv.Client(),
		Workdir:       root,
		Registry:      tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024, Cwd: cwd}, &tools.Write{Root: root, MaxBytes: tools.MaxWriteBytes, Cwd: cwd}, tools.UpdatePlan{}),
		Posture:       posture.Defaults(),
		AskDisabled:   true,
		AllowPassLoop: true,
		MaxIterations: 2,
		ReadOnlyAgent: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	goalStates := 0
	res, err := sess.run(context.Background(), nil, TurnInput{
		Prompt:      "execute the approved plan",
		ExecutePlan: true,
		PlanName:    "ship",
		ForceAgent:  "belai:debug",
	}, false, func(e Event) {
		if e.Kind == EventGoalStateKind {
			goalStates++
		}
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if goalStates == 0 {
		t.Fatal("plan execution must run the goal pass loop")
	}
	if res.GoalSentinel != rolemanager.GoalComplete {
		t.Fatalf("GoalSentinel = %q, want GOAL_COMPLETE", res.GoalSentinel)
	}
	body, _ := os.ReadFile(filepath.Join(root, "f.txt"))
	if !strings.HasPrefix(string(body), "pass ") {
		t.Fatalf("f.txt = %q: the plan's write must land despite read_only", body)
	}
}
