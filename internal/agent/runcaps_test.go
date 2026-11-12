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

	"github.com/vulnetix/belai/internal/audit"
	"github.com/vulnetix/belai/internal/budget"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
)

// capServer is a provider that calls Read on a new file every round and reports
// tokensPerCall tokens of usage for each. When obeyDirective is set it ends the
// turn with text as soon as it sees the run-cap directive.
type capServer struct {
	*httptest.Server
	mu         sync.Mutex
	calls      int
	directives int
	texts      []string
}

func newCapServer(t *testing.T, tokensPerCall int, obeyDirective bool) *capServer {
	t.Helper()
	cs := &capServer{}
	cs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var system, all string
		for _, m := range req.Messages {
			text := fmt.Sprint(m.Content)
			if m.Role == "system" {
				system = text
			}
			all += text + "\n"
		}
		write := func(msg map[string]any, usage map[string]any) {
			body := map[string]any{"id": "x", "object": "chat.completion",
				"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": "stop"}}}
			if usage != nil {
				body["usage"] = usage
			}
			b, _ := json.Marshal(body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(b)
		}
		switch {
		case strings.Contains(system, "security classifier"):
			write(map[string]any{"role": "assistant", "content": "SAFE"}, nil)
			return
		case strings.Contains(system, "operating-mode classifier"):
			write(map[string]any{"role": "assistant", "content": "AGENT"}, nil)
			return
		}
		if system == "" {
			// A call with no system block is not the session's model turn.
			write(map[string]any{"role": "assistant", "content": "SAFE"}, nil)
			return
		}
		cs.mu.Lock()
		cs.calls++
		n := cs.calls
		sawDirective := strings.Contains(all, "Run cap:")
		if sawDirective {
			cs.directives++
			cs.texts = append(cs.texts, all)
		}
		cs.mu.Unlock()
		usage := map[string]any{"prompt_tokens": tokensPerCall * 4 / 5, "completion_tokens": tokensPerCall - tokensPerCall*4/5, "total_tokens": tokensPerCall}
		if sawDirective && obeyDirective {
			write(map[string]any{"role": "assistant", "content": "wrapped up"}, usage)
			return
		}
		write(map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{
			"id": fmt.Sprintf("call_%d", n), "type": "function",
			"function": map[string]any{"name": "Read", "arguments": fmt.Sprintf(`{"path":"f%d.txt"}`, n)},
		}}}, usage)
	}))
	t.Cleanup(cs.Close)
	return cs
}

func (cs *capServer) callCount() int {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.calls
}

func capsSession(t *testing.T, cs *capServer, caps budget.Caps, o Options) (*Session, *budget.Meter, string) {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	root := t.TempDir()
	for i := 1; i < 40; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%d.txt", i)), []byte(fmt.Sprintf("file %d", i)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m := budget.NewMeter(caps)
	o.Cfg = run.Config{Provider: "openai", BaseURL: cs.URL, APIKey: "k", Model: "test"}
	o.Client = cs.Client()
	o.Registry = tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024})
	o.Posture = posture.Defaults()
	o.Workdir = root
	o.RunCaps = m
	sess, err := NewSession(o)
	if err != nil {
		t.Fatal(err)
	}
	return sess, m, root
}

type capRun struct {
	res    run.Result
	err    error
	asks   []*RunCapAsk
	warns  []string
	events []Event
}

// runCapTurn runs one agent turn and answers each run-cap ask with answer.
func runCapTurn(t *testing.T, sess *Session, answer func(*RunCapAsk) PermissionAskReply) capRun {
	t.Helper()
	var out capRun
	out.res, out.err = sess.run(context.Background(), nil, TurnInput{Prompt: "go through every file", ForceMode: modes.ModeAgent}, false, func(e Event) {
		out.events = append(out.events, e)
		switch e.Kind {
		case EventPermissionAskKind:
			if e.Ask != nil && e.Ask.RunCap != nil {
				out.asks = append(out.asks, e.Ask.RunCap)
				e.AskReply <- answer(e.Ask.RunCap)
			} else if e.AskReply != nil {
				e.AskReply <- PermissionAskReply{Allow: true}
			}
		case EventWarningKind:
			out.warns = append(out.warns, e.Warning)
		}
	})
	return out
}

var stopAnswer = func(*RunCapAsk) PermissionAskReply { return PermissionAskReply{Choice: RunCapStop} }

// R2 and R3: a run that crosses finalize_at is told once, with a fixed text that
// names the cap word and no number, and a model that obeys ends cleanly.
func TestRunCapsRule3_FinaliseDirectiveIsFixedText(t *testing.T) {
	cs := newCapServer(t, 450, true)
	sess, _, _ := capsSession(t, cs, budget.Caps{Tokens: 2000, MaxExtends: 3}, Options{AllowAsk: true})
	r := runCapTurn(t, sess, stopAnswer)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if len(r.asks) != 0 || r.res.StopReason == run.StopRunCap {
		t.Fatalf("a run that finalised was stopped: asks=%d stop=%q", len(r.asks), r.res.StopReason)
	}
	if r.res.Reply != "wrapped up" {
		t.Fatalf("reply %q", r.res.Reply)
	}
	if cs.callCount() != 5 {
		t.Fatalf("model calls = %d, want 4 rounds to reach 90%% and 1 to finalise", cs.callCount())
	}
	if cs.directives != 1 {
		t.Fatalf("the directive reached the model %d times", cs.directives)
	}
	text := cs.texts[0]
	i := strings.Index(text, "Run cap:")
	directive := text[i : i+len(runCapText("tokens"))]
	if directive != runCapText("tokens") {
		t.Fatalf("directive is not the fixed text:\n%s", directive)
	}
	for _, leak := range []string{"2000", "1800", "450"} {
		if strings.Contains(directive, leak) {
			t.Fatalf("the directive shows a number: %q", leak)
		}
	}
	warned := false
	for _, w := range r.warns {
		if strings.Contains(w, "told the model to finalise") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("no warning event: %v", r.warns)
	}
}

// R4 and R5: a run that goes from below the warning to the cap in one call is
// told, given two rounds, and then asked; stop ends it with no error.
func TestRunCapsRule5_UnwarnedHitGetsReserveThenAsks(t *testing.T) {
	cs := newCapServer(t, 1000, false)
	sess, m, _ := capsSession(t, cs, budget.Caps{Tokens: 2000, MaxExtends: 3}, Options{AllowAsk: true})
	r := runCapTurn(t, sess, stopAnswer)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.res.StopReason != run.StopRunCap {
		t.Fatalf("stop reason %q", r.res.StopReason)
	}
	if len(r.asks) != 1 || r.asks[0].Reason != budget.ReasonTokens || r.asks[0].ExtendsLeft != 3 || r.asks[0].Limit != 2000 || r.asks[0].ExtendBy != 1000 {
		t.Fatalf("asks %+v", r.asks)
	}
	// Two rounds to reach the cap, then the round that carried the directive and
	// one more.
	if cs.callCount() != 4 {
		t.Fatalf("model calls = %d", cs.callCount())
	}
	if cs.directives == 0 {
		t.Fatal("the run was never told to finalise")
	}
	if m.Snapshot().Tokens != 4000 {
		t.Fatalf("meter tokens %d", m.Snapshot().Tokens)
	}
}

// R4: extending raises the ceiling and the run goes on; the next hit asks again.
func TestRunCapsRule4_ExtendContinuesThenAsksAgain(t *testing.T) {
	cs := newCapServer(t, 450, false)
	sess, m, _ := capsSession(t, cs, budget.Caps{Tokens: 2000, MaxExtends: 1}, Options{AllowAsk: true})
	n := 0
	r := runCapTurn(t, sess, func(a *RunCapAsk) PermissionAskReply {
		n++
		return PermissionAskReply{Choice: RunCapExtend}
	})
	if r.err != nil || r.res.StopReason != run.StopRunCap {
		t.Fatalf("err=%v stop=%q", r.err, r.res.StopReason)
	}
	if n != 1 {
		t.Fatalf("asked %d times; with one extension the second hit stops without asking", n)
	}
	if m.Caps().Tokens != 3000 || m.Snapshot().Extends != 1 {
		t.Fatalf("caps %+v snapshot %+v", m.Caps(), m.Snapshot())
	}
}

// R4: anything but an explicit extend stops: an allow with no choice, an unknown
// choice, ask off, no ask permission, and a cancelled context.
func TestRunCapsRule4_OnlyAnExplicitExtendExtends(t *testing.T) {
	answers := map[string]func(*RunCapAsk) PermissionAskReply{
		"allow true, no choice": func(*RunCapAsk) PermissionAskReply { return PermissionAskReply{Allow: true} },
		"unknown choice":        func(*RunCapAsk) PermissionAskReply { return PermissionAskReply{Choice: "yes", Allow: true} },
		"stop":                  stopAnswer,
		"stop and keep":         func(*RunCapAsk) PermissionAskReply { return PermissionAskReply{Choice: RunCapStopKeep} },
	}
	for name, ans := range answers {
		cs := newCapServer(t, 1000, false)
		sess, m, _ := capsSession(t, cs, budget.Caps{Tokens: 2000, MaxExtends: 3}, Options{AllowAsk: true})
		r := runCapTurn(t, sess, ans)
		if r.err != nil || r.res.StopReason != run.StopRunCap || m.Snapshot().Extends != 0 || m.Caps().Tokens != 2000 {
			t.Errorf("%s: err=%v stop=%q extends=%d", name, r.err, r.res.StopReason, m.Snapshot().Extends)
		}
	}
	// Ask off and no ask permission: no ask is raised and the run stops.
	for name, o := range map[string]Options{
		"ask off":    {AllowAsk: true, AskDisabled: true},
		"cannot ask": {AllowAsk: false},
	} {
		cs := newCapServer(t, 1000, false)
		sess, m, _ := capsSession(t, cs, budget.Caps{Tokens: 2000, MaxExtends: 3}, o)
		r := runCapTurn(t, sess, func(*RunCapAsk) PermissionAskReply { return PermissionAskReply{Choice: RunCapExtend} })
		if len(r.asks) != 0 || r.res.StopReason != run.StopRunCap || m.Snapshot().Extends != 0 {
			t.Errorf("%s: asks=%d stop=%q extends=%d", name, len(r.asks), r.res.StopReason, m.Snapshot().Extends)
		}
	}
}

// E4: max_extends 0 stops without asking.
func TestRunCapsEdge4_NoExtensionsStopsWithoutAsking(t *testing.T) {
	cs := newCapServer(t, 1000, false)
	sess, _, _ := capsSession(t, cs, budget.Caps{Tokens: 2000, MaxExtends: 0}, Options{AllowAsk: true})
	r := runCapTurn(t, sess, func(*RunCapAsk) PermissionAskReply { return PermissionAskReply{Choice: RunCapExtend} })
	if len(r.asks) != 0 || r.res.StopReason != run.StopRunCap || r.err != nil {
		t.Fatalf("asks=%d stop=%q err=%v", len(r.asks), r.res.StopReason, r.err)
	}
}

// E5: a run whose extensions are used up stops at its next hit without asking.
func TestRunCapsEdge5_ExtensionsExhausted(t *testing.T) {
	cs := newCapServer(t, 450, false)
	sess, _, _ := capsSession(t, cs, budget.Caps{Tokens: 1000, MaxExtends: 2}, Options{AllowAsk: true})
	asks := 0
	r := runCapTurn(t, sess, func(*RunCapAsk) PermissionAskReply {
		asks++
		return PermissionAskReply{Choice: RunCapExtend}
	})
	if asks != 2 || r.res.StopReason != run.StopRunCap {
		t.Fatalf("asks=%d stop=%q", asks, r.res.StopReason)
	}
}

// R16: the ask is built from an enum and integers, whatever the model wrote.
func TestRunCapsRule16_AskTextIsEnumsAndIntegers(t *testing.T) {
	a := RunCapAsk{Reason: budget.ReasonCost, Used: 1_200_000, Limit: 1_000_000, ExtendBy: 500_000, ExtendsLeft: 2}
	want := "Run cap reached: cost $1.20 of $1.00. Extend by $0.50 (2 left), stop now, or stop and keep the counters?"
	if a.Text() != want {
		t.Fatalf("text %q", a.Text())
	}
	w := RunCapAsk{Reason: budget.ReasonWall, Used: 61_000, Limit: 60_000, ExtendBy: 30_000, ExtendsLeft: 1}
	if !strings.Contains(w.Text(), "wall 1m1s of 1m0s") {
		t.Fatalf("wall text %q", w.Text())
	}
	if RunCapChoice("allow").Valid() || RunCapChoice("").Valid() || !RunCapExtend.Valid() {
		t.Fatal("the choice set is not closed")
	}
	if _, err := NewRunCapFact("a cap the model made up"); err == nil {
		t.Fatal("an unknown reason made a fact")
	}
	for r := range FinalizeReasons {
		f, err := NewRunCapFact(r)
		if err != nil || !strings.Contains(f.Text(), "this session's "+r+" cap") || f.Event() != CoordRunCap {
			t.Fatalf("fact for %s: %v %q", r, err, f.Text())
		}
	}
}

// R9: a subagent spends against its parent's meter.
func TestRunCapsRule9_SubagentSpendCountsAgainstParent(t *testing.T) {
	cs := newCapServer(t, 300, true)
	sess, m, _ := capsSession(t, cs, budget.Caps{Tokens: 1_000_000, MaxExtends: 3}, Options{AllowAsk: true, AllowExplore: true})
	before := m.Snapshot().Tokens
	if _, err := sess.runTask(context.Background(), "look", "list the files"); err != nil {
		t.Fatal(err)
	}
	if got := m.Snapshot().Tokens - before; got < 300 {
		t.Fatalf("the subagent's calls added %d tokens to the parent's meter", got)
	}
}

// R9: a subagent that reaches a cap stops and never asks.
func TestRunCapsRule9_SubagentNeverAsks(t *testing.T) {
	cs := newCapServer(t, 1000, false)
	sess, _, _ := capsSession(t, cs, budget.Caps{Tokens: 500, MaxExtends: 3}, Options{AllowAsk: true})
	sess.exploreSubagent = true
	asked := false
	_, stop := sess.capBoundary(context.Background(), func(e Event) {
		if e.Kind == EventPermissionAskKind {
			asked = true
		}
	})
	if stop || asked {
		t.Fatal("below the cap: nothing to do")
	}
	sess.runCaps.Spend(0, 600)
	_, stop = sess.capBoundary(context.Background(), func(e Event) {
		if e.Kind == EventPermissionAskKind {
			asked = true
		}
	})
	if !stop || asked {
		t.Fatalf("stop=%v asked=%v", stop, asked)
	}
}

// R7: a model with no known price leaves the cost cap off and says so once.
func TestRunCapsRule7_UnpricedModelWarnsOnce(t *testing.T) {
	cs := newCapServer(t, 100, true)
	sess, m, _ := capsSession(t, cs, budget.Caps{CostMicroUSD: 1, MaxExtends: 3}, Options{AllowAsk: true})
	r := runCapTurn(t, sess, stopAnswer)
	if r.err != nil || r.res.StopReason == run.StopRunCap {
		t.Fatalf("an unpriced model hit a cost cap: err=%v stop=%q", r.err, r.res.StopReason)
	}
	n := 0
	for _, w := range r.warns {
		if strings.Contains(w, "no price is known") {
			n++
		}
	}
	if n != 1 || m.Snapshot().CostMicroUSD != 0 {
		t.Fatalf("warnings=%d cost=%d", n, m.Snapshot().CostMicroUSD)
	}
}

// R12: a stop clears the saved counters and a stop and keep leaves them.
func TestRunCapsRule12_StopForgetsKeepRemembers(t *testing.T) {
	for _, tc := range []struct {
		choice RunCapChoice
		kept   bool
	}{{RunCapStop, false}, {RunCapStopKeep, true}} {
		cs := newCapServer(t, 1000, false)
		sess, m, _ := capsSession(t, cs, budget.Caps{Tokens: 2000, MaxExtends: 3}, Options{AllowAsk: true})
		m.Persist("sess-caps")
		sess.sessionID = "sess-caps"
		choice := tc.choice
		decide := func(*RunCapAsk) PermissionAskReply { return PermissionAskReply{Choice: choice} }
		// Drive the decision directly so the turn's own end-of-turn save does not
		// stand between the choice and the file.
		m.Spend(0, 2500)
		if got := sess.capDecide(context.Background(), budget.ReasonTokens, func(e Event) {
			if e.Kind == EventPermissionAskKind {
				e.AskReply <- decide(e.Ask.RunCap)
			}
		}); got != tc.choice {
			t.Fatalf("%s: decided %q", tc.choice, got)
		}
		again := budget.NewMeter(budget.Caps{Tokens: 2000, MaxExtends: 3})
		restored := again.Persist("sess-caps")
		if tc.kept != (restored && again.Snapshot().Tokens == 2500) {
			t.Fatalf("%s: restored=%v snapshot=%+v", tc.choice, restored, again.Snapshot())
		}
		if !tc.kept && restored {
			t.Fatalf("%s: the counters were kept", tc.choice)
		}
	}
}

// R15: the audit events for a run cap hold the cap word, the outcome enum and
// the pass count only.
func TestRunCapsRule15_AuditEventIsEnumsOnly(t *testing.T) {
	rec, err := audit.Open(t.TempDir(), "test-host")
	if err != nil {
		t.Fatal(err)
	}
	audit.SetDefault(rec)
	t.Cleanup(func() { audit.SetDefault(nil) })
	cs := newCapServer(t, 450, false)
	sess, _, _ := capsSession(t, cs, budget.Caps{Tokens: 2000, MaxExtends: 1}, Options{AllowAsk: true})
	runCapTurn(t, sess, func(*RunCapAsk) PermissionAskReply { return PermissionAskReply{Choice: RunCapExtend} })
	b, err := os.ReadFile(rec.Path())
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, want := range []string{`"kind":"run_cap"`, `"outcome":"finalise"`, `"outcome":"extended"`, `"outcome":"stopped"`, `"reason":"tokens"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("audit lacks %s:\n%s", want, text)
		}
	}
	// The leak check reads the event fields only: the chain hashes and the
	// timestamp are hex and digits, so a cap value such as "2000" can turn up
	// in them by chance.
	var fields strings.Builder
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		var e audit.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("audit line %q: %v", line, err)
		}
		e.Seq, e.At, e.Prev, e.Hash = 0, 0, "", ""
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		fields.Write(b)
		fields.WriteByte('\n')
	}
	for _, leak := range []string{"f1.txt", "go through every file", "2000"} {
		if strings.Contains(fields.String(), leak) {
			t.Fatalf("the audit carries %q:\n%s", leak, text)
		}
	}
}

// R12: a whole turn that ends on Stop leaves no state file, and one that ends on
// Stop and keep leaves the counters.
func TestRunCapsRule12_WholeTurnStopAndKeep(t *testing.T) {
	for _, tc := range []struct {
		choice RunCapChoice
		kept   bool
	}{{RunCapStop, false}, {RunCapStopKeep, true}} {
		cs := newCapServer(t, 1000, false)
		sess, m, _ := capsSession(t, cs, budget.Caps{Tokens: 2000, MaxExtends: 3}, Options{AllowAsk: true})
		m.Persist("sess-turn")
		choice := tc.choice
		r := runCapTurn(t, sess, func(*RunCapAsk) PermissionAskReply { return PermissionAskReply{Choice: choice} })
		if r.res.StopReason != run.StopRunCap {
			t.Fatalf("%s: stop %q", tc.choice, r.res.StopReason)
		}
		again := budget.NewMeter(budget.Caps{Tokens: 2000, MaxExtends: 3})
		if got := again.Persist("sess-turn"); got != tc.kept {
			t.Fatalf("%s: restored=%v", tc.choice, got)
		}
	}
}

// E10: the cap is read at the same round boundary in every mode, so a plan or a
// goal turn ends on it the way an agent turn does.
func TestRunCapsEdge10_PlanAndGoalModes(t *testing.T) {
	for _, mode := range []modes.Mode{modes.ModePlan, modes.ModeGoal} {
		cs := newCapServer(t, 1000, false)
		sess, _, _ := capsSession(t, cs, budget.Caps{Tokens: 2000, MaxExtends: 0}, Options{AllowAsk: true, AllowPassLoop: true})
		var res run.Result
		var err error
		res, err = sess.run(context.Background(), nil, TurnInput{Prompt: "go through every file", ForceMode: mode}, false, func(e Event) {
			if e.Kind == EventPermissionAskKind && e.AskReply != nil {
				e.AskReply <- PermissionAskReply{}
			}
		})
		if err != nil || res.StopReason != run.StopRunCap {
			t.Errorf("%s: err=%v stop=%q reply=%q", mode, err, res.StopReason, res.Reply)
		}
	}
}

// E9: the pass ceiling is resilience.max_passes. The meter only warns about it;
// reaching it is the loop's own stop, never an ask.
func TestRunCapsEdge9_PassCeilingWarnsAndNeverAsks(t *testing.T) {
	cs := newCapServer(t, 10, false)
	sess, m, _ := capsSession(t, cs, budget.Caps{Passes: 10, Tokens: 1_000_000, MaxExtends: 3}, Options{AllowAsk: true})
	var events []Event
	emit := func(e Event) { events = append(events, e) }
	m.SetPasses(9)
	dir, stop := sess.capBoundary(context.Background(), emit)
	if stop || len(dir) == 0 {
		t.Fatalf("at 90%% of the pass ceiling: stop=%v directive=%d", stop, len(dir))
	}
	if dir2, _ := sess.capBoundary(context.Background(), emit); len(dir2) != 0 {
		t.Fatal("the warning was sent twice")
	}
	m.SetPasses(10)
	dir, stop = sess.capBoundary(context.Background(), emit)
	if stop || len(dir) != 0 {
		t.Fatalf("at the pass ceiling: stop=%v directive=%d", stop, len(dir))
	}
	for _, e := range events {
		if e.Kind == EventPermissionAskKind {
			t.Fatal("the pass ceiling raised an ask")
		}
	}
}
