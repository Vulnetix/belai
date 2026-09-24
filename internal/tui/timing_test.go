package tui

import (
	"testing"
	"time"

	"github.com/vulnetix/signet/internal/agent"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
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
