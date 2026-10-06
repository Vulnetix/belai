package tui

import (
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
)

// A turn's rows are written at turn end, but each must carry the time it
// happened and the duration of the work it reports: the transcript is the
// only record of where a 900-second turn went.
func TestTranscriptRowsCarryTheirOwnTimeAndDuration(t *testing.T) {
	a := newPersistApp(t)
	a.echoUser("run it")
	a.cancel = func() {} // a turn in flight holds its bubble until the end

	t0 := time.UnixMilli(1_790_000_000_000)
	t1 := t0.Add(1500 * time.Millisecond)
	t2 := t1.Add(2 * time.Second)
	t3 := t2.Add(3 * time.Second)

	a.handleAgentEvent(agentEventMsg{Kind: agent.EventModelCallKind, At: t1, Duration: 1500 * time.Millisecond})
	a.handleAgentEvent(agentEventMsg{Kind: agent.EventToolStartKind, At: t1, Tool: &rolemanager.ToolCall{ID: "c1", Name: "Bash", Args: map[string]any{"command": "go test"}}})
	a.handleAgentEvent(agentEventMsg{Kind: agent.EventToolResultKind, At: t2, ToolName: "Bash", ToolCallID: "c1", ToolResult: "ok", Duration: 2 * time.Second})
	a.handleAgentEvent(agentEventMsg{Kind: agent.EventModelCallKind, At: t3, Duration: 800 * time.Millisecond})
	a.handleAgentEvent(agentEventMsg{Kind: agent.EventTextKind, At: t3, Text: "done"})
	a.cancel = nil
	if last := a.trailingAssistant(); last >= 0 {
		a.messages[last].Materialise()
	}
	a.persistTailMode(true)

	entries := persistedEntries(t, a)
	byType := map[string][]int{}
	for i, e := range entries {
		byType[e.Type] = append(byType[e.Type], i)
	}
	if len(byType["tool"]) != 1 || len(byType["assistant"]) == 0 {
		t.Fatalf("entries = %+v", entries)
	}
	tool := entries[byType["tool"][0]]
	if tool.Timestamp != t1.UnixMilli() {
		t.Fatalf("tool row timestamp = %d, want its start %d", tool.Timestamp, t1.UnixMilli())
	}
	if got := tool.Meta["duration_ms"]; got != float64(2000) {
		t.Fatalf("tool duration_ms = %v, want 2000", got)
	}

	var calls []any
	for _, i := range byType["assistant"] {
		if c, ok := entries[i].Meta["model_calls_ms"].([]any); ok {
			calls = append(calls, c...)
		}
	}
	if len(calls) != 2 || calls[0] != float64(1500) || calls[1] != float64(800) {
		t.Fatalf("model_calls_ms = %v, want [1500 800]", calls)
	}

	seen := map[int64]bool{}
	for _, e := range entries {
		seen[e.Timestamp] = true
	}
	if len(seen) < 3 {
		t.Fatalf("rows share timestamps (flush time, not event time): %+v", entries)
	}
}

func TestRoleManagerRowsCarryTheirDuration(t *testing.T) {
	a := newPersistApp(t)
	a.echoUser("hi")
	at := time.UnixMilli(1_790_000_000_000)
	a.Update(rmActivityMsg(rolemanager.Activity{Event: rolemanager.EventSecuritySentinel, Verdict: "SAFE", Subject: "prompt", At: at, Duration: 420 * time.Millisecond}))
	a.persistTailMode(true)

	for _, e := range persistedEntries(t, a) {
		if e.Type != "rolemanager" {
			continue
		}
		if e.Timestamp != at.UnixMilli() || e.Meta["duration_ms"] != float64(420) {
			t.Fatalf("rolemanager entry = %+v", e)
		}
		return
	}
	t.Fatal("no rolemanager entry persisted")
}

func TestToolDurationFallsBackToTheRowSpan(t *testing.T) {
	start := time.UnixMilli(1000)
	if got := toolDurationMS(0, start, start.Add(250*time.Millisecond)); got != 250 {
		t.Fatalf("span = %d", got)
	}
	if got := toolDurationMS(time.Second, start, start); got != 1000 {
		t.Fatalf("measured = %d", got)
	}
	if got := toolDurationMS(0, time.Time{}, start); got != 0 {
		t.Fatalf("unknown start = %d", got)
	}
}

// The arguments of a call still streaming belong to that call. A finished
// tool row trailing the transcript must not absorb them: doing so glued the
// next call's JSON onto the previous row and persisted a tool_args that no
// longer parsed.
func TestToolCallDeltaDoesNotLeakIntoPreviousToolRow(t *testing.T) {
	a := newPersistApp(t)
	a.echoUser("run it")
	a.cancel = func() {}

	a.handleAgentEvent(agentEventMsg{Kind: agent.EventToolStartKind, Tool: &rolemanager.ToolCall{ID: "c1", Name: "Bash", Args: map[string]any{"command": "go test"}}})
	a.handleAgentEvent(agentEventMsg{Kind: agent.EventToolResultKind, ToolName: "Bash", ToolCallID: "c1", ToolResult: "ok"})
	want := a.messages[len(a.messages)-1].ToolArgs

	a.handleAgentEvent(agentEventMsg{Kind: agent.EventToolCallDeltaKind, ToolDelta: &run.ToolCallDelta{Index: 0, ID: "c2", Name: "Bash", Args: `{"command": "git diff"}`}})
	a.handleAgentEvent(agentEventMsg{Kind: agent.EventToolCallDeltaKind, ToolDelta: &run.ToolCallDelta{Index: 0, Args: `{"more": 1}`}})

	if got := a.messages[len(a.messages)-1].ToolArgs; got != want {
		t.Fatalf("finished row args = %q, want %q", got, want)
	}
}

func rowTimes(a *App) []time.Time {
	var out []time.Time
	for _, m := range a.messages {
		if m.Role != "user" {
			out = append(out, m.CreatedAt)
		}
	}
	return out
}

// A decision made before a tool result can reach the thread after it, because
// agent events and decisions are read from separate channels. The thread is
// time ordered whichever arrives first, and nothing is written twice or lost.
func TestARoleManagerRowArrivingLateIsPlacedByTime(t *testing.T) {
	a := newPersistApp(t)
	a.echoUser("run it")
	a.cancel = func() {} // a turn in flight

	t0 := time.UnixMilli(1_790_000_000_000)
	a.handleAgentEvent(agentEventMsg{Kind: agent.EventToolStartKind, At: t0.Add(1 * time.Second), Tool: &rolemanager.ToolCall{ID: "c1", Name: "Bash", Args: map[string]any{"command": "ls"}}})
	a.handleAgentEvent(agentEventMsg{Kind: agent.EventToolResultKind, At: t0.Add(3 * time.Second), ToolName: "Bash", ToolCallID: "c1", ToolResult: "ok"})
	a.persistTail()
	// The decision about the call was made at t0+500ms and arrives last.
	a.Update(rmActivityMsg(rolemanager.Activity{Event: rolemanager.EventSecuritySentinel, Verdict: "SAFE", Subject: "Bash", At: t0.Add(500 * time.Millisecond)}))
	a.cancel = nil
	a.persistTailMode(true)

	times := rowTimes(a)
	for i := 1; i < len(times); i++ {
		if times[i].Before(times[i-1]) {
			t.Fatalf("the thread is not time ordered at row %d: %v", i, times)
		}
	}
	if a.messages[1].Role != "rolemanager" {
		t.Fatalf("the decision was not placed before the tool call: %v", rolesOf(a))
	}

	// Each row is written once: the cursor still counts the moved row.
	var counts = map[string]int{}
	for _, e := range persistedEntries(t, a) {
		counts[e.Type]++
	}
	if counts["user"] != 1 || counts["assistant"] != 1 || counts["tool"] != 1 || counts["rolemanager"] != 1 {
		t.Fatalf("entries = %v, want one of each", counts)
	}
}

func rolesOf(a *App) []string {
	var out []string
	for _, m := range a.messages {
		out = append(out, m.Role)
	}
	return out
}

// A row never crosses a prompt: an earlier turn's rows stay where they are.
func TestAPlacedRowNeverCrossesAUserPrompt(t *testing.T) {
	a := newPersistApp(t)
	t0 := time.UnixMilli(1_790_000_000_000)
	a.echoUser("first")
	a.Update(rmActivityMsg(rolemanager.Activity{Event: rolemanager.EventSecuritySentinel, Verdict: "SAFE", Subject: "prompt", At: t0.Add(5 * time.Second)}))
	a.persistTailMode(true)
	a.echoUser("second")
	// An old timestamp, as from a straggler of the first turn.
	a.Update(rmActivityMsg(rolemanager.Activity{Event: rolemanager.EventSecuritySentinel, Verdict: "SAFE", Subject: "prompt", At: t0}))
	roles := rolesOf(a)
	if roles[len(roles)-2] != "user" || roles[len(roles)-1] != "rolemanager" {
		t.Fatalf("rows = %v, the row crossed the prompt", roles)
	}
}

// An in-order row stays where it was appended.
func TestAnInOrderRoleManagerRowStaysPut(t *testing.T) {
	a := newPersistApp(t)
	a.echoUser("hi")
	t0 := time.UnixMilli(1_790_000_000_000)
	a.Update(rmActivityMsg(rolemanager.Activity{Event: rolemanager.EventSecuritySentinel, Verdict: "SAFE", Subject: "a", At: t0}))
	a.Update(rmActivityMsg(rolemanager.Activity{Event: rolemanager.EventSecuritySentinel, Verdict: "SAFE", Subject: "b", At: t0.Add(time.Second)}))
	times := rowTimes(a)
	if len(times) != 2 || !times[0].Equal(t0) || !times[1].Equal(t0.Add(time.Second)) {
		t.Fatalf("times = %v", times)
	}
}

// In the chronological layout a notice an agent event raised is placed by the
// event's time too, but a notice that still has to be written never moves behind
// the persistence cursor, so it is written once.
func TestChronologicalLayoutPlacesNoticesButNeverBehindTheCursor(t *testing.T) {
	a := newPersistApp(t)
	a.settings.UI = &config.UISettings{Layout: ptrString(config.LayoutChronological)}
	a.echoUser("run it")
	a.cancel = func() {}

	t0 := time.UnixMilli(1_790_000_000_000)
	// A decision stamped late is already in the thread.
	a.Update(rmActivityMsg(rolemanager.Activity{Event: rolemanager.EventSecuritySentinel, Verdict: "SAFE", Subject: "prompt", At: t0.Add(5 * time.Second)}))
	// An older event raises a warning: it was emitted before that decision.
	a.handleAgentEvent(agentEventMsg{Kind: agent.EventWarningKind, At: t0.Add(1 * time.Second), Warning: "slow provider"})
	a.cancel = nil
	a.persistTailMode(true)

	times := rowTimes(a)
	for i := 1; i < len(times); i++ {
		if times[i].Before(times[i-1]) {
			t.Fatalf("the thread is not time ordered: %v (%v)", times, rolesOf(a))
		}
	}
	counts := map[string]int{}
	for _, e := range persistedEntries(t, a) {
		counts[e.Type]++
	}
	if counts["system"] != 1 || counts["rolemanager"] != 1 || counts["user"] != 1 {
		t.Fatalf("entries = %v, want one of each", counts)
	}
}

// The clean layout leaves notices where the event stream put them.
func TestCleanLayoutLeavesNoticesInArrivalOrder(t *testing.T) {
	a := newPersistApp(t)
	a.echoUser("run it")
	a.cancel = func() {}
	t0 := time.UnixMilli(1_790_000_000_000)
	a.Update(rmActivityMsg(rolemanager.Activity{Event: rolemanager.EventSecuritySentinel, Verdict: "SAFE", Subject: "prompt", At: t0.Add(5 * time.Second)}))
	a.handleAgentEvent(agentEventMsg{Kind: agent.EventWarningKind, At: t0.Add(1 * time.Second), Warning: "slow provider"})
	a.cancel = nil
	roles := rolesOf(a)
	if roles[len(roles)-1] != "system" {
		t.Fatalf("rows = %v, the notice moved in the clean layout", roles)
	}
}

func ptrString(s string) *string { return &s }
