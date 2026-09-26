package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/filediff"
	"github.com/vulnetix/belai/internal/tui/components"
)

// toolRowFor finds the tool row for a call id. Indices are not assumed: New
// seeds the transcript, so the row's position is not fixed.
func toolRowFor(t *testing.T, a *App, callID string) *components.Message {
	t.Helper()
	for i := range a.messages {
		if a.messages[i].Role == "tool" && a.messages[i].ToolCallID == callID {
			return &a.messages[i]
		}
	}
	t.Fatalf("no tool row for %q", callID)
	return nil
}

// appWithRunningTool returns an App holding one in-flight Bash tool row.
func appWithRunningTool(callID string) *App {
	a := New(Options{Provider: "openai", Model: "gpt-5"})
	a.messages = append(a.messages,
		components.Message{Role: "user", Content: "build it"},
		components.Message{
			Role:       "tool",
			ToolName:   "Bash",
			ToolArgs:   `{"command":"make"}`,
			ToolCallID: callID,
			StartedAt:  time.Now(),
		})
	return a
}

// TestToolProgressLandsOnItsOwnRow: progress is keyed by call id, so two tools
// running at once cannot have their output spliced together.
func TestToolProgressLandsOnItsOwnRow(t *testing.T) {
	a := appWithRunningTool("call-a")
	a.messages = append(a.messages, components.Message{
		Role: "tool", ToolName: "Bash", ToolArgs: `{"command":"test"}`,
		ToolCallID: "call-b", StartedAt: time.Now(),
	})

	a.handleAgentEvent(agentEventMsg{
		Kind: agent.EventToolProgressKind, ToolCallID: "call-a", ToolProgress: "for a",
	})
	a.handleAgentEvent(agentEventMsg{
		Kind: agent.EventToolProgressKind, ToolCallID: "call-b", ToolProgress: "for b",
	})

	rowA, rowB := toolRowFor(t, a, "call-a"), toolRowFor(t, a, "call-b")
	if !rowA.HasProgress() || !rowB.HasProgress() {
		t.Fatal("both rows should have progress")
	}
	linesA, _ := rowA.ProgressTail(5)
	linesB, _ := rowB.ProgressTail(5)
	if strings.Join(linesA, "") != "for a" || strings.Join(linesB, "") != "for b" {
		t.Fatalf("progress crossed rows: a=%v b=%v", linesA, linesB)
	}
}

// TestToolProgressNeverReachesTheModel is the guard that keeps this feature
// render-only: the provider-facing transcript must be byte-identical whether
// or not a tool streamed anything.
func TestToolProgressNeverReachesTheModel(t *testing.T) {
	before := appWithRunningTool("call-1").buildTurns()

	a := appWithRunningTool("call-1")
	for _, line := range []string{"compiling", "linking", "secret-looking output"} {
		a.handleAgentEvent(agentEventMsg{
			Kind: agent.EventToolProgressKind, ToolCallID: "call-1", ToolProgress: line,
		})
	}
	after := a.buildTurns()

	if len(before) != len(after) {
		t.Fatalf("progress changed the turn count: %d then %d", len(before), len(after))
	}
	for i := range before {
		if before[i].Content != after[i].Content || before[i].Role != after[i].Role {
			t.Fatalf("turn %d changed:\n before %+v\n  after %+v", i, before[i], after[i])
		}
	}
	for _, turn := range after {
		if strings.Contains(turn.Content, "compiling") {
			t.Fatalf("streamed output leaked into a provider turn: %+v", turn)
		}
	}
}

// TestToolDiffNeverReachesTheModel: a diff is observed around a tool rather
// than returned by it, so it must not alter the provider-facing transcript.
func TestToolDiffNeverReachesTheModel(t *testing.T) {
	before := appWithRunningTool("call-1").buildTurns()

	a := appWithRunningTool("call-1")
	a.handleAgentEvent(agentEventMsg{
		Kind: agent.EventToolResultKind, ToolCallID: "call-1", ToolName: "Bash", ToolResult: "",
	})
	a.handleAgentEvent(agentEventMsg{
		Kind: agent.EventToolDiffKind, ToolCallID: "call-1",
		Diff: &filediff.Change{Files: []filediff.FileChange{{
			Path: "secret.go", Old: "before\n", New: "after\n",
		}}},
	})

	row := toolRowFor(t, a, "call-1")
	if row.Diff() == nil {
		t.Fatal("diff did not land on the row")
	}

	after := a.buildTurns()
	if len(before) != len(after) {
		t.Fatalf("diff changed the turn count: %d then %d", len(before), len(after))
	}
	for _, turn := range after {
		if strings.Contains(turn.Content, "secret.go") || strings.Contains(turn.Content, "after") {
			t.Fatalf("diff leaked into a provider turn: %+v", turn)
		}
	}
}

// TestNextAgentCoalescesProgress: without this, every flush from a chatty
// command is its own Bubble Tea update and therefore its own full transcript
// re-render.
func TestNextAgentCoalescesProgress(t *testing.T) {
	a := appWithRunningTool("call-1")
	ch := make(chan agent.Event, 256)
	a.events = ch

	const n = 100
	for i := 0; i < n; i++ {
		ch <- agent.Event{Kind: agent.EventToolProgressKind, ToolCallID: "call-1", ToolProgress: "line"}
	}

	msg := a.nextAgent()()
	ev, ok := msg.(agentEventMsg)
	if !ok {
		t.Fatalf("want an agentEventMsg, got %T", msg)
	}
	if ev.Kind != agent.EventToolProgressKind {
		t.Fatalf("kind = %v", ev.Kind)
	}
	if got := strings.Count(ev.ToolProgress, "\n") + 1; got != n {
		t.Fatalf("coalesced %d lines, want %d", got, n)
	}
	if len(ch) != 0 {
		t.Fatalf("%d events left undrained", len(ch))
	}
}

// TestNextAgentDoesNotCoalesceAcrossToolCalls: two concurrent tools must not
// have their progress merged into one event, or it would land on one row.
func TestNextAgentDoesNotCoalesceAcrossToolCalls(t *testing.T) {
	a := appWithRunningTool("call-1")
	ch := make(chan agent.Event, 8)
	a.events = ch

	ch <- agent.Event{Kind: agent.EventToolProgressKind, ToolCallID: "call-1", ToolProgress: "a"}
	ch <- agent.Event{Kind: agent.EventToolProgressKind, ToolCallID: "call-2", ToolProgress: "b"}

	ev := a.nextAgent()().(agentEventMsg)
	if ev.ToolCallID != "call-1" || ev.ToolProgress != "a" {
		t.Fatalf("first event = %+v", ev)
	}
	// The second must be replayed, not dropped.
	ev2 := a.nextAgent()().(agentEventMsg)
	if ev2.ToolCallID != "call-2" || ev2.ToolProgress != "b" {
		t.Fatalf("second event = %+v", ev2)
	}
}

// TestNextAgentStopsCoalescingAtResult: the run ends at the first event of
// another kind, which must be replayed rather than swallowed.
func TestNextAgentStopsCoalescingAtResult(t *testing.T) {
	a := appWithRunningTool("call-1")
	ch := make(chan agent.Event, 8)
	a.events = ch

	ch <- agent.Event{Kind: agent.EventToolProgressKind, ToolCallID: "call-1", ToolProgress: "a"}
	ch <- agent.Event{Kind: agent.EventToolProgressKind, ToolCallID: "call-1", ToolProgress: "b"}
	ch <- agent.Event{Kind: agent.EventToolResultKind, ToolCallID: "call-1", ToolName: "Bash", ToolResult: "a\nb"}

	ev := a.nextAgent()().(agentEventMsg)
	if ev.ToolProgress != "a\nb" {
		t.Fatalf("coalesced progress = %q", ev.ToolProgress)
	}
	ev2 := a.nextAgent()().(agentEventMsg)
	if ev2.Kind != agent.EventToolResultKind {
		t.Fatalf("result event was lost, got kind %v", ev2.Kind)
	}
}

// TestToolProgressSurvivesAModalView: the pump re-arms on message type rather
// than on the active view, so opening settings mid-command must not stall the
// drain and deadlock the subprocess against a full channel.
func TestToolProgressSurvivesAModalView(t *testing.T) {
	a := appWithRunningTool("call-1")
	a.view = viewSettings

	cmd := a.handleAgentEvent(agentEventMsg{
		Kind: agent.EventToolProgressKind, ToolCallID: "call-1", ToolProgress: "still going",
	})
	if cmd == nil {
		t.Fatal("handler must re-arm the pump even under a modal view")
	}
	if !toolRowFor(t, a, "call-1").HasProgress() {
		t.Fatal("progress was dropped while a modal view was open")
	}
}

var _ tea.Cmd // keep the bubbletea import honest if the file is trimmed
