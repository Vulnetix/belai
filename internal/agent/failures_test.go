package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/todos"
	"github.com/vulnetix/belai/internal/tools"
)

func TestFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		result     string
		repairable bool
		verdict    bool
	}{
		{`tool call rejected: bad argument`, true, false},
		{executionError("Todo", errors.New("no text")), true, false},
		{`tool result withheld: malformed arguments for "Read": unexpected end`, true, false},
		{truncatedArgsPrefix + "; re-issue", true, false},
		{`tool result withheld: classified injection. do not retry`, false, true},
		{`tool result withheld: permission denied for "Write"`, false, true},
		{`tool result withheld: denied by hook`, false, true},
		{`tool result withheld: "Edit" is outside the handoff scope`, false, true},
		{`ok: 3 matches`, false, false},
	} {
		if got := repairable(tc.result); got != tc.repairable {
			t.Errorf("repairable(%q) = %v, want %v", tc.result, got, tc.repairable)
		}
		if got := verdict(tc.result); got != tc.verdict {
			t.Errorf("verdict(%q) = %v, want %v", tc.result, got, tc.verdict)
		}
	}
}

// todoSession builds a session whose registry holds Todo and whose provider is
// the given server. allowLoop turns the harness's turn-level loops on.
func todoSession(t *testing.T, srv *httptest.Server, maxIter int, allowLoop bool) *Session {
	t.Helper()
	root := t.TempDir()
	cwd := tools.NewCwd(root)
	cfg := run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "test-key", Model: "test"}
	sess, err := NewSession(Options{
		Cfg:           cfg,
		Client:        srv.Client(),
		Workdir:       root,
		Registry:      tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024, Cwd: cwd}, tools.Todo{}, tools.UpdatePlan{}),
		Posture:       posture.Defaults(),
		MaxIterations: maxIter,
		AllowPassLoop: allowLoop,
		AskDisabled:   true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return sess
}

func scriptedServer(t *testing.T, step func(n int, w http.ResponseWriter)) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	var n int
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only chat completions advance the script; the nonce fetch is not a
		// model turn.
		if r.URL.Path != "/chat/completions" {
			writeChatJSON(w, "")
			return
		}
		mu.Lock()
		n++
		cur := n
		mu.Unlock()
		step(cur, w)
	}))
}

func runPass(t *testing.T, sess *Session, mode modes.Mode) (passOutcome, []run.Turn) {
	t.Helper()
	pipe := rolemanager.NewPipeline(run.NewClassifier(sess.cfg, sess.client))
	out, turns, err := sess.pass(context.Background(), pipe, "", nil, false, func(Event) {}, mode)
	if err != nil {
		t.Fatalf("pass: %v", err)
	}
	return out, turns
}

// A Todo call the model keeps getting wrong is an error in the call, not a
// verdict: it is fed back with a repair directive, counted apart from the
// withheld streak, never counted as work, and bounded.
func TestRepairableFailuresAreFedBackAndBounded(t *testing.T) {
	srv := scriptedServer(t, func(n int, w http.ResponseWriter) {
		writeToolCallJSON(w, "Todo", `{"todos":[{"status":"pending"}]}`)
	})
	defer srv.Close()
	sess := todoSession(t, srv, 10, false)

	out, turns := runPass(t, sess, modes.ModeAgent)
	if !out.exhausted || out.repairFailures != maxRepairIterations {
		t.Fatalf("out = %+v, want exhausted with repairFailures == %d", out, maxRepairIterations)
	}
	if out.withheld != 0 || out.productive != 0 {
		t.Fatalf("a repairable failure is neither a verdict nor work: %+v", out)
	}
	if sess.turnToolRuns != 0 {
		t.Fatalf("turnToolRuns = %d, want 0", sess.turnToolRuns)
	}
	var directive bool
	for _, turn := range turns {
		if strings.Contains(turn.Content, "errors in your own tool calls") {
			directive = true
		}
		if strings.Contains(turn.Content, "Writes are unavailable") {
			t.Fatalf("a repairable failure must not get the plan-mode surrender: %q", turn.Content)
		}
	}
	if !directive {
		t.Fatal("no repair directive was injected")
	}
}

// The corrected call after an error succeeds, and the list is adopted.
func TestRepairableFailureThenCorrectedCallRecovers(t *testing.T) {
	srv := scriptedServer(t, func(n int, w http.ResponseWriter) {
		switch n {
		case 1:
			writeToolCallJSON(w, "Todo", `{"todos":[{"explanation":"","status":"pending"}]}`)
		case 2:
			writeToolCallJSON(w, "Todo", `{"todos":[{"content":"ship it","status":"completed"}]}`)
		default:
			writeChatJSON(w, "All done.")
		}
	})
	defer srv.Close()
	sess := todoSession(t, srv, 10, false)

	out, _ := runPass(t, sess, modes.ModeAgent)
	if out.exhausted || out.reply != "All done." {
		t.Fatalf("out = %+v, want a natural exit", out)
	}
	if !sess.turnHasTodos || len(sess.turnTodos.Items) != 1 || sess.turnTodos.HasOpen() {
		t.Fatalf("turn todos = %+v", sess.turnTodos)
	}
}

// A call to a tool that was not offered no longer ends the turn under the abort
// policy: every call of the response is answered and the model re-issues.
func TestUnofferedToolIsFedBackNotFatal(t *testing.T) {
	srv := scriptedServer(t, func(n int, w http.ResponseWriter) {
		if n == 1 {
			writeToolCallJSON(w, "Rea", `{"path":"f.txt"}`)
			return
		}
		writeChatJSON(w, "Recovered.")
	})
	defer srv.Close()
	sess := todoSession(t, srv, 10, false)

	out, turns := runPass(t, sess, modes.ModeAgent)
	if out.reply != "Recovered." {
		t.Fatalf("reply = %q", out.reply)
	}
	var fed bool
	for _, turn := range turns {
		if turn.Role == "tool" && strings.Contains(turn.Content, "not a tool you were offered") && strings.Contains(turn.Content, "Read") {
			fed = true
		}
	}
	if !fed {
		t.Fatal("the unavailable tool name was not answered with the closest offered tool")
	}
}

// The abort policy still ends a model that keeps naming tools it was not given.
func TestUnofferedToolStillAbortsAfterRepeatedFeedback(t *testing.T) {
	srv := scriptedServer(t, func(n int, w http.ResponseWriter) {
		writeToolCallJSON(w, "Nope", `{}`)
	})
	defer srv.Close()
	sess := todoSession(t, srv, 10, false)
	pipe := rolemanager.NewPipeline(run.NewClassifier(sess.cfg, sess.client))
	if _, _, err := sess.pass(context.Background(), pipe, "", nil, false, func(Event) {}, modes.ModeAgent); err == nil {
		t.Fatal("a model that keeps naming unavailable tools must end the turn")
	}
}

type panicTool struct{}

func (panicTool) Definition() tools.Definition {
	return tools.Definition{Name: "Boom", Description: "panics"}
}
func (panicTool) Kind() tools.Kind              { return tools.KindTodo }
func (panicTool) Subject(map[string]any) string { return "" }
func (panicTool) Mutates() bool                 { return false }
func (panicTool) Execute(context.Context, map[string]any) (tools.Result, error) {
	panic("kaboom")
}

// A panic in a tool is an execution error the model can read, never a crash.
func TestToolPanicBecomesExecutionError(t *testing.T) {
	srv := scriptedServer(t, func(n int, w http.ResponseWriter) { writeChatJSON(w, "x") })
	defer srv.Close()
	sess := todoSession(t, srv, 3, false)
	sess.registry = sess.registry.With(panicTool{})

	out := sess.executeCall(context.Background(), rolemanager.ToolCall{ID: "c", Name: "Boom", Args: map[string]any{}}, func(Event) {}, nil)
	if !strings.HasPrefix(out, executionErrorPrefix) || !strings.Contains(out, "failed unexpectedly") || !repairable(out) {
		t.Fatalf("panic result = %q", out)
	}
}

// A text-only reply with todos open is sent back, bounded, and the list is the
// turn's own.
func TestOpenTodosSendTheTurnBack(t *testing.T) {
	srv := scriptedServer(t, func(n int, w http.ResponseWriter) {
		switch n {
		case 1:
			writeToolCallJSON(w, "Todo", `{"todos":[{"content":"a","status":"in_progress"},{"content":"b","status":"pending"}]}`)
		case 2:
			writeChatJSON(w, "I am done.")
		case 3:
			writeToolCallJSON(w, "Todo", `{"todos":[{"content":"a","status":"completed"},{"content":"b","status":"completed"}]}`)
		default:
			writeChatJSON(w, "Final report: a and b are done.")
		}
	})
	defer srv.Close()
	sess := todoSession(t, srv, 10, true)

	out, turns := runPass(t, sess, modes.ModeAgent)
	if out.reply != "Final report: a and b are done." {
		t.Fatalf("reply = %q", out.reply)
	}
	if sess.todoReprompts != 1 || sess.turnTodos.HasOpen() {
		t.Fatalf("reprompts = %d, open = %v", sess.todoReprompts, sess.turnTodos.Open())
	}
	var sent bool
	for _, turn := range turns {
		if strings.Contains(turn.Content, "2 of 2 todos are still open") {
			sent = true
		}
	}
	if !sent {
		t.Fatal("the open-todo directive was not injected")
	}
}

// A model that will not finish its list is stopped at the bound, not argued with.
func TestOpenTodoSendBackIsBounded(t *testing.T) {
	srv := scriptedServer(t, func(n int, w http.ResponseWriter) {
		if n == 1 {
			writeToolCallJSON(w, "Todo", `{"todos":[{"content":"a","status":"pending"}]}`)
			return
		}
		writeChatJSON(w, "Stopping here.")
	})
	defer srv.Close()
	sess := todoSession(t, srv, 20, true)

	out, _ := runPass(t, sess, modes.ModeAgent)
	if out.reply != "Stopping here." || sess.todoReprompts != maxTodoReprompts {
		t.Fatalf("reply = %q, reprompts = %d, want %d", out.reply, sess.todoReprompts, maxTodoReprompts)
	}
}

// Plan mode, a session that cannot loop and a read-only turn are never sent back.
func TestOpenTodoGuardSkips(t *testing.T) {
	srv := scriptedServer(t, func(n int, w http.ResponseWriter) {})
	defer srv.Close()
	for name, setup := range map[string]func(*Session) modes.Mode{
		"plan":      func(s *Session) modes.Mode { return modes.ModePlan },
		"no loop":   func(s *Session) modes.Mode { s.allowPassLoop = false; return modes.ModeAgent },
		"read only": func(s *Session) modes.Mode { s.turnReadOnly = true; return modes.ModeAgent },
		"explore":   func(s *Session) modes.Mode { s.exploreSubagent = true; return modes.ModeAgent },
		"report":    func(s *Session) modes.Mode { s.reportOnly = true; return modes.ModeAgent },
	} {
		sess := todoSession(t, srv, 3, true)
		sess.turnTodos, sess.turnHasTodos = todos.New("", []string{"a"}), true
		mode := setup(sess)
		if _, _, again := sess.refuseOpenTodos(mode); again {
			t.Errorf("%s: the guard must not send the turn back", name)
		}
	}
	sess := todoSession(t, srv, 3, true)
	sess.turnTodos, sess.turnHasTodos = todos.New("", []string{"a"}), true
	if open, total, again := sess.refuseOpenTodos(modes.ModeAgent); !again || open != 1 || total != 1 {
		t.Fatalf("agent turn with an open todo: %d/%d %v", open, total, again)
	}
}

// An agent turn that did work and ended on a fragment is asked for its report.
func TestAgentTurnEndsWithAReport(t *testing.T) {
	srv := scriptedServer(t, func(n int, w http.ResponseWriter) {
		writeChatJSON(w, "Changed a.go to return the sum; go test passes.")
	})
	defer srv.Close()
	sess := todoSession(t, srv, 3, true)
	sess.turnToolRuns, sess.turnMutations = 3, 1
	sess.lastTurns = []run.Turn{{Role: "user", Content: "do it"}}

	res := sess.agentFinish(context.Background(), "", false, func(Event) {}, run.Result{Reply: "ok"}, false)
	if res.Reply != "Changed a.go to return the sum; go test passes." {
		t.Fatalf("reply = %q, want the report", res.Reply)
	}
}

func TestAgentTurnThatChangedNothingKeepsItsAnswer(t *testing.T) {
	srv := scriptedServer(t, func(n int, w http.ResponseWriter) { t.Error("no report turn expected") })
	defer srv.Close()
	sess := todoSession(t, srv, 3, true)
	sess.turnToolRuns = 5 // reads and searches only: no file changed
	res := sess.agentFinish(context.Background(), "", false, func(Event) {}, run.Result{Reply: "4"}, false)
	if res.Reply != "4" {
		t.Fatalf("a question's answer must be kept, got %q", res.Reply)
	}
}

// A report that cannot be written leaves a harness-composed line of facts.
func TestAgentReportFallsBackToHarnessFacts(t *testing.T) {
	srv := scriptedServer(t, func(n int, w http.ResponseWriter) { writeChatJSON(w, "") })
	defer srv.Close()
	sess := todoSession(t, srv, 3, true)
	sess.turnToolRuns = 2
	sess.turnTodos, sess.turnHasTodos = todos.New("", []string{"a", "b"}), true
	sess.turnTodos.Items[0].Status = todos.StatusDone
	sess.lastTurns = []run.Turn{{Role: "user", Content: "do it"}}

	res := sess.agentFinish(context.Background(), "", false, func(Event) {}, run.Result{}, true)
	if !strings.Contains(res.Reply, "2 tool call(s)") || !strings.Contains(res.Reply, "1 of 2 done") {
		t.Fatalf("fallback report = %q", res.Reply)
	}
}

func TestLedgerRefusesOpenTodosThenReportsThem(t *testing.T) {
	l := passLedger{goalText: "g", hasList: true, list: todos.New("g", []string{"a", "b"})}
	for i := 0; i < maxTodoReprompts; i++ {
		if open, total, again := l.refuseOpenTodos(); !again || open != 2 || total != 2 {
			t.Fatalf("send-back %d: %d/%d %v", i, open, total, again)
		}
	}
	if _, _, again := l.refuseOpenTodos(); again {
		t.Fatal("the send-back is bounded")
	}
	var warned bool
	l.settleTodos(func(e Event) {
		if e.Kind == EventWarningKind && strings.Contains(e.Warning, "2 todo(s) still open") {
			warned = true
		}
	})
	if !warned || len(l.list.Open()) != 2 {
		t.Fatalf("an unfinished list is reported open, never marked done: warned=%v list=%+v", warned, l.list)
	}
}
