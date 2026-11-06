package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/session"
)

const breakerSummary = "## Goal\nship\n## Next Steps\n1. build\n## Critical Context\npath=/x"

func intp(v int) *int { return &v }

// breakerSession is a session over a 1000 token window that holds a long
// conversation, with the breaker set to open after two failures and rest for
// three boundaries.
func breakerSession() (*Session, *[]Event) {
	var events []Event
	s := &Session{
		cfg: run.Config{Model: "test"},
		settings: config.Settings{
			ContextWindows: map[string]int{"test": 1000},
			Compaction:     &config.CompactionSettings{BreakerFailures: intp(2), BreakerCooldownPasses: intp(3)},
		},
		live: posture.NewLive(posture.Defaults(), false),
	}
	s.emit = func(e Event) { events = append(events, e) }
	return s, &events
}

func longTurns() []run.Turn {
	long := strings.Repeat("a", 8000) // ~2000 estimated tokens
	return []run.Turn{
		{Role: "user", Content: "do the thing"},
		{Role: "assistant", Content: long},
		{Role: "tool", Content: long, ToolCallID: "c1", ToolName: "Read"},
	}
}

// countingPipe returns a pipeline whose summary calls are counted and whose
// summary step fails while *down is set.
func countingPipe(calls *atomic.Int32, down *atomic.Bool) *rolemanager.Pipeline {
	return rolemanager.NewPipeline(rolemanager.ClassifierFunc(func(_ context.Context, p rolemanager.ClassifierPayload) (string, error) {
		if strings.Contains(p.System, "summarization") {
			calls.Add(1)
			if down.Load() {
				return "", errors.New("down")
			}
			return breakerSummary, nil
		}
		return "SAFE", nil
	}))
}

func TestBreakerOpensAfterConsecutiveFailuresThenRests(t *testing.T) {
	s, events := breakerSession()
	var calls atomic.Int32
	var down atomic.Bool
	down.Store(true)
	pipe := countingPipe(&calls, &down)
	ctx := context.Background()

	for i := 1; i <= 2; i++ {
		if _, ok := s.compactBoundary(ctx, pipe, longTurns()); ok {
			t.Fatalf("boundary %d compacted with the classifier down", i)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("summary calls = %d, want 2", calls.Load())
	}
	if st := s.CompactionState(); st.Fails != 2 || st.CooldownLeft != 3 {
		t.Fatalf("state after opening = %+v", st)
	}
	warnings := 0
	for _, e := range *events {
		if e.Kind == EventWarningKind && strings.Contains(e.Warning, "compaction paused") {
			warnings++
		}
	}
	if warnings != 1 {
		t.Fatalf("opening warnings = %d, want exactly one", warnings)
	}

	// Three boundaries are skipped without a classifier call, even though the
	// classifier is back.
	down.Store(false)
	for i := 1; i <= 3; i++ {
		if _, ok := s.compactBoundary(ctx, pipe, longTurns()); ok {
			t.Fatalf("paused boundary %d compacted", i)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("a paused boundary called the classifier: %d calls", calls.Load())
	}
	// The next boundary retries, and the success closes the breaker.
	if _, ok := s.compactBoundary(ctx, pipe, longTurns()); !ok {
		t.Fatal("the boundary after the cooldown did not retry")
	}
	if st := s.CompactionState(); st.Fails != 0 || st.CooldownLeft != 0 {
		t.Fatalf("a success did not reset the breaker: %+v", st)
	}
	if calls.Load() != 3 {
		t.Fatalf("summary calls = %d, want 3", calls.Load())
	}
}

func TestBreakerHalfOpenFailureReopensAtOnce(t *testing.T) {
	s, _ := breakerSession()
	var calls atomic.Int32
	var down atomic.Bool
	down.Store(true)
	pipe := countingPipe(&calls, &down)
	ctx := context.Background()
	for i := 0; i < 2+3; i++ { // open, then rest
		s.compactBoundary(ctx, pipe, longTurns())
	}
	before := calls.Load()
	s.compactBoundary(ctx, pipe, longTurns()) // the retry fails
	if calls.Load() != before+1 {
		t.Fatalf("the retry did not call the classifier")
	}
	if st := s.CompactionState(); st.CooldownLeft != 3 {
		t.Fatalf("one failure after the cooldown must reopen: %+v", st)
	}
}

func TestBreakerSuccessBetweenFailuresKeepsItClosed(t *testing.T) {
	s, _ := breakerSession()
	var calls atomic.Int32
	var down atomic.Bool
	pipe := countingPipe(&calls, &down)
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		down.Store(i%2 == 0) // fail, succeed, fail, succeed
		s.compactBoundary(ctx, pipe, longTurns())
	}
	if st := s.CompactionState(); st.CooldownLeft != 0 || st.Fails != 0 {
		t.Fatalf("alternating results opened the breaker: %+v", st)
	}
}

func TestBreakerCountsEachSummaryFailureKind(t *testing.T) {
	cases := map[string]rolemanager.Classifier{
		"error":     rolemanager.ClassifierFunc(func(context.Context, rolemanager.ClassifierPayload) (string, error) { return "", errors.New("x") }),
		"malformed": rolemanager.ClassifierFunc(func(context.Context, rolemanager.ClassifierPayload) (string, error) { return "no sections", nil }),
		"rejected": rolemanager.ClassifierFunc(func(_ context.Context, p rolemanager.ClassifierPayload) (string, error) {
			if strings.Contains(p.System, "summarization") {
				return breakerSummary, nil
			}
			return "UNSAFE", nil
		}),
	}
	for name, c := range cases {
		s, _ := breakerSession()
		s.compactBoundary(context.Background(), rolemanager.NewPipeline(c), longTurns())
		if st := s.CompactionState(); st.Fails != 1 {
			t.Errorf("%s: fails = %d, want 1", name, st.Fails)
		}
	}
}

func TestBreakerIgnoresACancelledTurn(t *testing.T) {
	s, _ := breakerSession()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pipe := rolemanager.NewPipeline(rolemanager.ClassifierFunc(func(context.Context, rolemanager.ClassifierPayload) (string, error) {
		return "", context.Canceled
	}))
	s.compactBoundary(ctx, pipe, longTurns())
	if st := s.CompactionState(); st.Fails != 0 {
		t.Fatalf("a cancelled turn counted as a summary failure: %+v", st)
	}
}

func TestBreakerDoesNotStopForcedRecovery(t *testing.T) {
	s, _ := breakerSession()
	var calls atomic.Int32
	var down atomic.Bool
	down.Store(true)
	pipe := countingPipe(&calls, &down)
	ctx := context.Background()
	s.compactBoundary(ctx, pipe, longTurns())
	s.compactBoundary(ctx, pipe, longTurns())
	if s.CompactionState().CooldownLeft == 0 {
		t.Fatal("precondition: the breaker is open")
	}
	down.Store(false)
	before := calls.Load()
	got, ok := s.recoverOverflow(ctx, pipe, longTurns())
	if !ok || len(got) != 2 || calls.Load() != before+1 {
		t.Fatalf("forced recovery did not summarise through an open breaker: ok=%v turns=%d calls=%d", ok, len(got), calls.Load()-before)
	}
	if st := s.CompactionState(); st.CooldownLeft != 0 || st.Fails != 0 {
		t.Fatalf("a forced success should close the breaker: %+v", st)
	}
}

func TestBreakerOpenStillPrunes(t *testing.T) {
	var calls atomic.Int32
	var down atomic.Bool
	pipe := countingPipe(&calls, &down)

	s := pruneSession(&promptDecider{keep: map[string]bool{"t1": true}}, 6000)
	// The breaker is open: the summary step is paused.
	out, ok := s.compactTurns(context.Background(), pipe, convo(20), false, true)
	if !ok || !s.lastCompactionPruned || len(out) <= 2 {
		t.Fatalf("pruning stopped with the breaker open: ok=%v pruned=%v len=%d", ok, s.lastCompactionPruned, len(out))
	}
	if calls.Load() != 0 {
		t.Fatal("the summary ran with the breaker open")
	}
	// A prune that cannot answer falls back to the summary, which stays paused.
	broken := pruneSession(&promptDecider{err: &decisions.Error{Class: decisions.ClassUnavailable, Status: 503}}, 6000)
	if _, ok := broken.compactTurns(context.Background(), pipe, convo(20), false, true); ok {
		t.Fatal("the summary ran with the breaker open")
	}
	if calls.Load() != 0 {
		t.Fatal("the summary classifier was called with the breaker open")
	}
}

func TestPruneShortfallIsNotASummaryFailure(t *testing.T) {
	var calls atomic.Int32
	var down atomic.Bool
	pipe := countingPipe(&calls, &down)
	// The prune cannot answer, so the summary runs and succeeds: nothing counts.
	s := pruneSession(&promptDecider{err: &decisions.Error{Class: decisions.ClassUnavailable, Status: 503}}, 6000)
	s.settings.Compaction = &config.CompactionSettings{BreakerFailures: intp(1)}
	if _, ok := s.compactTurns(context.Background(), pipe, convo(20), false, false); !ok {
		t.Fatal("summary fallback did not run")
	}
	if st := s.CompactionState(); st.Fails != 0 || st.CooldownLeft != 0 {
		t.Fatalf("state = %+v", st)
	}
}

func TestBreakerStateIsRestoredClamped(t *testing.T) {
	s, events := breakerSession()
	s.RestoreCompactionState(session.CompactionState{Fails: 5, CooldownLeft: 1 << 20, Tokens: -4, Window: 128000})
	st := s.CompactionState()
	if st.Fails != 5 || st.CooldownLeft != 20 || st.Tokens != 0 || st.Window != 128000 {
		t.Fatalf("restored state = %+v", st)
	}
	// A restored cooldown pauses the summary step on the next boundaries.
	var calls atomic.Int32
	var down atomic.Bool
	if _, ok := s.compactBoundary(context.Background(), countingPipe(&calls, &down), longTurns()); ok || calls.Load() != 0 {
		t.Fatal("a restored cooldown did not pause the summary")
	}
	found := false
	for _, e := range *events {
		if e.Kind == EventCompactionStateKind && e.Compaction != nil && e.Compaction.CooldownLeft == 19 {
			found = true
		}
	}
	if !found {
		t.Fatalf("the cooldown shrinking was not reported: %+v", *events)
	}
}

func TestCompactThresholdFollowsTheSetting(t *testing.T) {
	var calls atomic.Int32
	var down atomic.Bool
	pipe := countingPipe(&calls, &down)
	// longTurns is about 4000 estimated tokens: 50 percent of an 8000 window.
	mk := func(pct *int) *Session {
		s := &Session{
			cfg:      run.Config{Model: "test"},
			settings: config.Settings{ContextWindows: map[string]int{"test": 8000}},
			live:     posture.NewLive(posture.Defaults(), false),
		}
		if pct != nil {
			s.settings.Compaction = &config.CompactionSettings{ThresholdPct: pct}
		}
		return s
	}
	if _, ok := mk(nil).compactBoundary(context.Background(), pipe, longTurns()); ok {
		t.Fatal("the default 70 percent threshold compacted a 50 percent context")
	}
	if _, ok := mk(intp(40)).compactBoundary(context.Background(), pipe, longTurns()); !ok {
		t.Fatal("threshold_pct 40 did not compact a 50 percent context")
	}
	if _, ok := mk(intp(95)).compactBoundary(context.Background(), pipe, longTurns()); ok {
		t.Fatal("threshold_pct 95 compacted a 50 percent context")
	}
}

func TestKeepRecentTurnsFollowsTheSetting(t *testing.T) {
	turns := convo(20)
	base := pruneSession(nil, 6000)
	wide := pruneSession(nil, 6000)
	wide.settings.Compaction = &config.CompactionSettings{KeepRecentTurns: intp(30)}
	candidates := func(s *Session) int {
		n := 0
		for _, p := range s.collectPairs(turns) {
			if !p.pinned {
				n++
			}
		}
		return n
	}
	if candidates(wide) >= candidates(base) {
		t.Fatalf("keep_recent_turns 30 left %d candidates, the default %d", candidates(wide), candidates(base))
	}
}

func TestKeepResultCharsFollowsTheSetting(t *testing.T) {
	turns := []run.Turn{{Role: "user", Content: "go"}}
	turns = append(turns, callPair("c1", "Read", map[string]any{"file_path": "a.go"}, strings.Repeat("y", 2000))...)
	pairs := []prunePair{{id: "t1", callTurn: 1, callID: "c1", tool: "Read", resTurn: 2, chars: 2000}}
	scores := map[string]float64{"r:t1": 0, "c:t1": 1}
	for _, head := range []int{0, 300, 1000} {
		out, _, truncated := applyPrune(turns, pairs, scores, head)
		if truncated != 1 {
			t.Fatalf("head %d: truncated %d", head, truncated)
		}
		var res string
		for _, tr := range out {
			if tr.Role == "tool" {
				res = tr.Content
			}
		}
		if !strings.HasPrefix(res, strings.Repeat("y", head)) || strings.HasPrefix(res, strings.Repeat("y", head+1)) {
			t.Fatalf("head %d: result keeps the wrong amount: %q", head, res[:min(len(res), 40)])
		}
		if !strings.Contains(res, fmt.Sprintf("pruned %d characters", 2000-head)) {
			t.Fatalf("head %d: note = %q", head, res[min(len(res), head):])
		}
	}
}

func TestKeepToolResultsFollowsTheSetting(t *testing.T) {
	s := &Session{
		cfg:      run.Config{Model: "test"},
		settings: config.Settings{ContextWindows: map[string]int{"test": 1 << 20}, Compaction: &config.CompactionSettings{KeepToolResults: intp(0)}},
		live:     posture.NewLive(posture.Defaults(), false),
	}
	turns := []run.Turn{{Role: "user", Content: "go"}}
	for i := range 4 {
		turns = append(turns, run.Turn{Role: "tool", Content: "result", ToolCallID: fmt.Sprintf("c%d", i), ToolName: "Bash"})
	}
	failing := rolemanager.NewPipeline(rolemanager.ClassifierFunc(func(context.Context, rolemanager.ClassifierPayload) (string, error) {
		return "", errors.New("down")
	}))
	got, ok := s.recoverOverflow(context.Background(), failing, turns)
	if !ok {
		t.Fatal("clearing did not run")
	}
	for _, tr := range got[1:] {
		if tr.Content != run.ClearedToolResult {
			t.Fatalf("keep_tool_results 0 left a result: %q", tr.Content)
		}
	}
}
