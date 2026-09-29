package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/rolemanager/jev"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
)

// call builds an assistant turn holding one tool call, and its result turn.
func callPair(id, tool string, args map[string]any, result string) []run.Turn {
	return []run.Turn{
		{Role: "assistant", ToolCalls: []rolemanager.ToolCall{{ID: id, Name: tool, Args: args}}},
		{Role: "tool", Content: result, ToolCallID: id, ToolName: tool},
	}
}

// convo is a user request, n Read pairs with big results, and a recent tail.
func convo(n int) []run.Turn {
	turns := []run.Turn{{Role: "user", Content: "fix the login bug"}}
	for i := 0; i < n; i++ {
		turns = append(turns, callPair(fmt.Sprintf("c%d", i), "Read", map[string]any{"file_path": fmt.Sprintf("f%d.go", i)}, strings.Repeat("x", 2000))...)
	}
	// The newest turns are pinned.
	for i := 0; i < pruneKeepRecent; i++ {
		turns = append(turns, run.Turn{Role: "assistant", Content: fmt.Sprintf("thinking %d", i)})
	}
	return turns
}

func pruneSession(d decisions.Decider, window int) *Session {
	s := &Session{
		cfg:      run.Config{Model: "test"},
		settings: config.Settings{ContextWindows: map[string]int{"test": window}},
		live:     posture.NewLive(posture.Defaults(), false),
	}
	if d != nil {
		s.jev = &jev.Jobs{Client: jev.NewWith(d)}
	}
	return s
}

func TestCollectPairsPinsWhatMustStay(t *testing.T) {
	turns := []run.Turn{{Role: "user", Content: "request"}}
	turns = append(turns, callPair("a", "Read", map[string]any{"file_path": "a.go"}, "file a")...)             // candidate
	turns = append(turns, callPair("b", "Write", map[string]any{"file_path": "b.go"}, "wrote")...)             // mutates
	turns = append(turns, callPair("c", "Bash", map[string]any{"command": "false"}, "boom\nexit status 1")...) // failure
	turns = append(turns, callPair("d", "Skill", map[string]any{"skill": "x"}, "instructions")...)             // instructions
	turns = append(turns, callPair("e", "update_plan", nil, "ok")...)                                          // state
	turns = append(turns, callPair("f", "mcp__srv__tool", nil, "mcp")...)                                      // mcp
	turns = append(turns, callPair("g", "Read", map[string]any{"file_path": "g.go"}, "withheld")...)
	turns[len(turns)-1].Content = "tool result withheld: classified"
	// A call with no result and a result with no call are left alone.
	turns = append(turns, run.Turn{Role: "assistant", ToolCalls: []rolemanager.ToolCall{{ID: "orphan", Name: "Read"}}})
	turns = append(turns, run.Turn{Role: "tool", Content: "stray", ToolCallID: "nocall", ToolName: "Read"})
	turns = append(turns, callPair("h", "Read", map[string]any{"file_path": "h.go"}, "another")...) // candidate
	for i := 0; i < pruneKeepRecent; i++ {
		turns = append(turns, run.Turn{Role: "assistant", Content: "tail"})
	}
	// A call in a turn carrying signed thinking is never pruned.
	thought := callPair("i", "Read", map[string]any{"file_path": "i.go"}, "thought")
	thought[0].Thinking = []run.ThinkingBlock{{Thinking: "t", Signature: "s"}}
	turns = append(turns[:len(turns)-pruneKeepRecent], append(thought, turns[len(turns)-pruneKeepRecent:]...)...)

	s := &Session{registry: tools.Default(t.TempDir(), false)}
	pairs := s.collectPairs(turns)
	pinned := map[string]bool{}
	for _, p := range pairs {
		pinned[p.callID] = p.pinned
	}
	for id, want := range map[string]bool{"a": false, "b": true, "c": true, "d": true, "e": true, "f": true, "g": true, "h": false, "i": true} {
		if got, ok := pinned[id]; !ok || got != want {
			t.Errorf("pair %s pinned = %v (present %v), want %v", id, got, ok, want)
		}
	}
	if _, ok := pinned["orphan"]; ok {
		t.Error("a call with no result was paired")
	}
	if len(pairs) != 9 {
		t.Errorf("pairs = %d, want 9", len(pairs))
	}
}

func TestTheFirstRequestAndTheNewestTurnsAreNeverCandidates(t *testing.T) {
	turns := []run.Turn{{Role: "user", Content: "first"}}
	turns = append(turns, callPair("first", "Read", nil, "early")...)
	for i := 0; i < 3; i++ {
		turns = append(turns, callPair(fmt.Sprintf("m%d", i), "Read", nil, "mid")...)
	}
	turns = append(turns, callPair("late", "Read", nil, "recent")...)
	s := &Session{}
	for _, p := range s.collectPairs(turns) {
		if p.callID == "late" && !p.pinned {
			t.Error("a pair among the newest turns is a candidate")
		}
	}
}

func TestStateShowsSizesNeverResults(t *testing.T) {
	turns := convo(6)
	s := &Session{}
	state, goal, ok := pruneState(turns, s.collectPairs(turns), 24_000)
	if !ok {
		t.Fatal("state did not fit")
	}
	st := state.String()
	if strings.Contains(st, "xxxxxxxx") {
		t.Fatal("a tool result's text reached the state")
	}
	if !strings.Contains(st, `"result":"ok, 2000 chars (omitted)"`) || !strings.Contains(st, `"id":"t1"`) {
		t.Fatalf("state = %.400s", st)
	}
	if goal.String() != "fix the login bug" {
		t.Fatalf("goal = %q", goal.String())
	}
}

func TestStateFitsTheBudgetByShrinkingOldestFirst(t *testing.T) {
	turns := []run.Turn{{Role: "user", Content: "request"}}
	for i := 0; i < 40; i++ {
		turns = append(turns, run.Turn{Role: "assistant", Content: strings.Repeat("word ", 300)})
		turns = append(turns, callPair(fmt.Sprintf("c%d", i), "Read", map[string]any{"file_path": strings.Repeat("dir/", 30) + "f.go"}, "r")[0])
		turns = append(turns, callPair(fmt.Sprintf("c%d", i), "Read", nil, "r")[1])
	}
	for i := 0; i < pruneKeepRecent; i++ {
		turns = append(turns, run.Turn{Role: "assistant", Content: "recent tail text"})
	}
	s := &Session{}
	pairs := s.collectPairs(turns)
	for _, budget := range []int{60_000, 20_000, 8_000} {
		state, _, ok := pruneState(turns, pairs, budget)
		if !ok {
			t.Fatalf("budget %d: did not fit", budget)
		}
		if state.Len() > budget+budget/50 {
			t.Fatalf("budget %d: state is %d bytes", budget, state.Len())
		}
		// The request and the newest turns are always in full.
		if !strings.Contains(state.String(), `"text":"request"`) || !strings.Contains(state.String(), "recent tail text") {
			t.Fatalf("budget %d: a pinned message was shrunk", budget)
		}
	}
	if _, _, ok := pruneState(turns, pairs, 200); ok {
		t.Fatal("a state that cannot fit was reported as fitting")
	}
}

func TestApplyPruneDecisions(t *testing.T) {
	turns := convo(4)
	s := &Session{}
	pairs := s.collectPairs(turns)
	// t1: keep both; t2: keep call, cut result; t3: drop; t4: unknown, so keep.
	scores := map[string]float64{
		"c:t1": 0.9, "r:t1": 0.9,
		"c:t2": 0.8, "r:t2": 0.2,
		"c:t3": 0.1, "r:t3": 0.05,
	}
	out, dropped, truncated := applyPrune(turns, pairs, scores)
	if dropped != 1 || truncated != 1 {
		t.Fatalf("dropped %d truncated %d", dropped, truncated)
	}
	byID := map[string]string{}
	calls := map[string]bool{}
	for _, tt := range out {
		if tt.Role == "tool" {
			byID[tt.ToolCallID] = tt.Content
		}
		for _, c := range tt.ToolCalls {
			calls[c.ID] = true
		}
	}
	if len(byID["c0"]) != 2000 {
		t.Errorf("a kept result changed: %d chars", len(byID["c0"]))
	}
	if r := byID["c1"]; !strings.HasPrefix(r, strings.Repeat("x", pruneHeadChars)) || !strings.Contains(r, "pruned 1700 characters") || len(r) > pruneHeadChars+200 {
		t.Errorf("truncated result = %.120q (len %d)", r, len(r))
	}
	if _, ok := byID["c2"]; ok || calls["c2"] {
		t.Error("the dropped pair is still present")
	}
	if len(byID["c3"]) != 2000 {
		t.Error("an unanswered pair was changed")
	}
	// Providers need matching pairs: every kept call has its result and back.
	for id := range calls {
		if _, ok := byID[id]; !ok {
			t.Errorf("call %s lost its result", id)
		}
	}
	for id := range byID {
		if !calls[id] {
			t.Errorf("result %s lost its call", id)
		}
	}
}

func TestApplyPruneKeepsThingsThatMustNotChange(t *testing.T) {
	turns := []run.Turn{{Role: "user", Content: "go"}}
	turns = append(turns, callPair("short", "Read", nil, "tiny")...)
	turns = append(turns, callPair("offloaded", "Bash", nil, strings.Repeat("y", 900)+"\n[use ReadResult r3 for the rest]")...)
	turns = append(turns, callPair("cleared", "Read", nil, run.ClearedToolResult)...)
	for i := 0; i < pruneKeepRecent; i++ {
		turns = append(turns, run.Turn{Role: "assistant", Content: "tail"})
	}
	s := &Session{}
	pairs := s.collectPairs(turns)
	low := map[string]float64{}
	for _, p := range pairs {
		low["r:"+p.id], low["c:"+p.id] = 0.1, 0.9 // keep the call, drop the result
	}
	out, dropped, truncated := applyPrune(turns, pairs, low)
	if dropped != 0 || truncated != 0 {
		t.Fatalf("dropped %d truncated %d: a short, offloaded or cleared result was cut", dropped, truncated)
	}
	if len(out) != len(turns) {
		t.Fatalf("turns changed: %d -> %d", len(turns), len(out))
	}
}

func TestAnAssistantMessageWithNothingLeftIsRemovedButTextIsKept(t *testing.T) {
	turns := []run.Turn{{Role: "user", Content: "go"}}
	turns = append(turns, callPair("bare", "Read", nil, strings.Repeat("z", 500))...)
	withText := callPair("texty", "Read", nil, strings.Repeat("z", 500))
	withText[0].Content = "I will read it now."
	turns = append(turns, withText...)
	for i := 0; i < pruneKeepRecent; i++ {
		turns = append(turns, run.Turn{Role: "assistant", Content: "tail"})
	}
	s := &Session{}
	pairs := s.collectPairs(turns)
	drop := map[string]float64{}
	for _, p := range pairs {
		drop["r:"+p.id], drop["c:"+p.id] = 0, 0
	}
	out, dropped, _ := applyPrune(turns, pairs, drop)
	if dropped != 2 {
		t.Fatalf("dropped = %d", dropped)
	}
	var texts []string
	for _, tt := range out {
		if tt.Role == "assistant" && len(tt.ToolCalls) == 0 && tt.Content != "tail" {
			texts = append(texts, tt.Content)
		}
		if tt.Role == "tool" {
			t.Errorf("a tool turn survived: %+v", tt)
		}
	}
	if len(texts) != 1 || texts[0] != "I will read it now." {
		t.Fatalf("remaining assistant text = %q", texts)
	}
}

// promptDecider scores each prune statement: pairs listed in keep get a high
// score, the rest a low one.
type promptDecider struct {
	mu    sync.Mutex
	keep  map[string]bool
	calls int
	err   error
}

func (d *promptDecider) Decide(_ context.Context, r decisions.Request) (decisions.Result, error) {
	d.mu.Lock()
	d.calls++
	d.mu.Unlock()
	if d.err != nil {
		return decisions.Result{}, d.err
	}
	labels := r.State.(map[string]any)["items"].(map[string]string)
	out := map[string]decisions.Answer{}
	for q := range r.Questions {
		id := strings.TrimPrefix(q, "s:")
		score := 0.05
		for keepID := range d.keep {
			if strings.Contains(labels[id], "tool call "+keepID+" ") {
				score = 0.9
			}
		}
		out[q] = decisions.Answer{Type: decisions.TypeNoul, Noul: score}
	}
	return decisions.Result{Answers: out}, nil
}
func (d *promptDecider) Identity() string           { return "fake/systemone" }
func (d *promptDecider) Backend() decisions.Backend { return decisions.BackendSystemOne }

func TestPruneTurnsBringsTheContextUnderTheTrigger(t *testing.T) {
	turns := convo(20) // ~10k estimated tokens
	d := &promptDecider{keep: map[string]bool{"t1": true}}
	s := pruneSession(d, 6000)
	out, st, ok := s.pruneTurns(context.Background(), turns)
	if !ok {
		t.Fatalf("prune failed: %+v", st)
	}
	if st.dropped != 19 || st.kept != 1 || st.tokensAfter >= st.tokensBefore {
		t.Fatalf("stats = %+v", st)
	}
	if len(out) >= len(turns) {
		t.Fatalf("turns %d -> %d", len(turns), len(out))
	}
}

func TestPruneFallsBackWhenItFreesTooLittle(t *testing.T) {
	turns := convo(20)
	// The backend wants to keep everything.
	all := map[string]bool{}
	for i := 1; i <= 20; i++ {
		all[fmt.Sprintf("t%d", i)] = true
	}
	s := pruneSession(&promptDecider{keep: all}, 6000)
	if _, _, ok := s.pruneTurns(context.Background(), turns); ok {
		t.Fatal("a prune that removed nothing reported success")
	}
	// Dropping a little is not enough when the context is still over the trigger.
	few := map[string]bool{}
	for i := 3; i <= 20; i++ {
		few[fmt.Sprintf("t%d", i)] = true
	}
	if _, st, ok := s.pruneTurns(context.Background(), turns); ok && st.tokensAfter*100 >= 6000*compactThresholdPct {
		t.Fatalf("accepted a prune still over the trigger: %+v", st)
	}
}

func TestPruneUnavailableBackendChangesNothing(t *testing.T) {
	turns := convo(20)
	s := pruneSession(&promptDecider{err: &decisions.Error{Class: decisions.ClassUnavailable, Status: 503}}, 6000)
	if out, _, ok := s.pruneTurns(context.Background(), turns); ok || out != nil {
		t.Fatal("an unavailable backend pruned")
	}
	auth := pruneSession(&promptDecider{err: &decisions.Error{Class: decisions.ClassAuth, Status: 401}}, 6000)
	if _, _, ok := auth.pruneTurns(context.Background(), turns); ok {
		t.Fatal("a refused credential pruned")
	}
	if _, _, ok := pruneSession(nil, 6000).pruneTurns(context.Background(), turns); ok {
		t.Fatal("no backend pruned")
	}
	off := pruneSession(&promptDecider{}, 6000)
	off.jev.On = func(j config.JevJob) bool { return j != config.JevPruneCompaction }
	if _, _, ok := off.pruneTurns(context.Background(), turns); ok {
		t.Fatal("a switched-off job pruned")
	}
}

// compactTurns prunes first and keeps the conversation (so the caller must not
// restate the request), and falls back to the summary when the prune cannot.
func TestCompactTurnsPrunesThenFallsBackToTheSummary(t *testing.T) {
	summary := "## Goal\nship\n## Next Steps\n1. build\n## Critical Context\npath=/x"
	classifier := rolemanager.ClassifierFunc(func(_ context.Context, p rolemanager.ClassifierPayload) (string, error) {
		if strings.Contains(p.System, "summarization") {
			return summary, nil
		}
		return "SAFE", nil
	})
	pipe := rolemanager.NewPipeline(classifier)

	s := pruneSession(&promptDecider{keep: map[string]bool{"t1": true}}, 6000)
	turns := convo(20)
	out, ok := s.compactTurns(context.Background(), pipe, turns, false)
	if !ok || !s.lastCompactionPruned || len(out) <= 2 || out[0].Content != "fix the login bug" {
		t.Fatalf("expected a prune that keeps the conversation: ok %v pruned %v len %d", ok, s.lastCompactionPruned, len(out))
	}

	broken := pruneSession(&promptDecider{err: &decisions.Error{Class: decisions.ClassUnavailable, Status: 503}}, 6000)
	out, ok = broken.compactTurns(context.Background(), pipe, convo(20), false)
	if !ok || broken.lastCompactionPruned || len(out) != 2 {
		t.Fatalf("expected the summary fallback: ok %v pruned %v len %d", ok, broken.lastCompactionPruned, len(out))
	}
}

func TestPruneRecordsCountsOnly(t *testing.T) {
	var got []rolemanager.Activity
	cancel := rolemanager.AddSink(func(a rolemanager.Activity) {
		if a.Event == rolemanager.EventPruneCompaction {
			got = append(got, a)
		}
	})
	defer cancel()
	pipe := rolemanager.NewPipeline(rolemanager.ClassifierFunc(func(context.Context, rolemanager.ClassifierPayload) (string, error) { return "SAFE", nil }))
	s := pruneSession(&promptDecider{keep: map[string]bool{"t1": true}}, 6000)
	turns := convo(20)
	if _, ok := s.compactTurns(context.Background(), pipe, turns, false); !ok {
		t.Fatal("no prune")
	}
	if len(got) != 1 || got[0].Verdict != "pruned" {
		t.Fatalf("recorded = %+v", got)
	}
	d, _ := rolemanager.Describe(got[0])
	if !strings.Contains(d.Outcome, "dropped 19") || strings.Contains(got[0].Detail, "f1.go") {
		t.Fatalf("outcome %q detail %q", d.Outcome, got[0].Detail)
	}
	b, _ := json.Marshal(got[0])
	if strings.Contains(string(b), "login") {
		t.Fatal("conversation text reached the record")
	}
}
