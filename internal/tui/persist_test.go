package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/agent"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/session"
	"github.com/vulnetix/signet/internal/tui/components"
)

// newPersistApp builds an App with an isolated store and a workdir. Startup
// notices are dropped so the persistence tests assert only what they append.
func newPersistApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := New(Options{Provider: "openai", Model: "gpt-5", Workdir: t.TempDir()})
	a.messages = nil
	a.persistedUpTo = 0
	return a
}

func persistedEntries(t *testing.T, a *App) []session.Entry {
	t.Helper()
	entries, err := a.store.Read(a.workdir, a.sessionID)
	if err != nil {
		t.Fatalf("read persisted entries: %v", err)
	}
	return entries
}

func TestPersistToolPairWritesMatchingIDs(t *testing.T) {
	a := newPersistApp(t)
	a.echoUser("run it")
	a.messages = append(a.messages,
		components.Message{Role: "assistant", ToolCalls: []components.AgentToolCall{{ID: "call-1", Name: "Bash", Args: `{"command":"ls"}`}}},
		components.Message{Role: "tool", ToolName: "Bash", ToolArgs: `{"command":"ls"}`, ToolCallID: "call-1", Content: "result", Status: "✓"},
	)
	a.persistTail()

	entries := persistedEntries(t, a)
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3: %+v", len(entries), entries)
	}
	if entries[0].Type != "user" || entries[1].Type != "assistant" || entries[2].Type != "tool" {
		t.Fatalf("entry types = %q/%q/%q", entries[0].Type, entries[1].Type, entries[2].Type)
	}
	calls, ok := entries[1].Meta["tool_calls"].([]any)
	if !ok || len(calls) != 1 {
		t.Fatalf("assistant tool_calls = %#v", entries[1].Meta["tool_calls"])
	}
	call := calls[0].(map[string]any)
	if call["id"] != "call-1" || call["name"] != "Bash" {
		t.Fatalf("call = %#v", call)
	}
	if entries[2].Meta["tool_call_id"] != "call-1" {
		t.Fatalf("tool entry tool_call_id = %#v", entries[2].Meta["tool_call_id"])
	}
	if entries[1].ParentID != entries[0].ID || entries[2].ParentID != entries[1].ID {
		t.Fatalf("parent chain broken: %s -> %s -> %s", entries[0].ID, entries[1].ParentID, entries[2].ParentID)
	}
}

func TestPersistNothingPastStillRunningTool(t *testing.T) {
	a := newPersistApp(t)
	a.echoUser("run it")
	a.messages = append(a.messages,
		components.Message{Role: "assistant", ToolCalls: []components.AgentToolCall{{ID: "call-1", Name: "Bash", Args: `{"command":"ls"}`}}},
		components.Message{Role: "tool", ToolName: "Bash", ToolArgs: `{"command":"ls"}`, ToolCallID: "call-1"},
	)
	a.persistTail()

	entries := persistedEntries(t, a)
	// Only the user turn; the assistant tool_calls entry must wait for its
	// result so the file never holds an unpaired call.
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1 (user only): %+v", len(entries), entries)
	}
}

func TestPersistTailIdempotent(t *testing.T) {
	a := newPersistApp(t)
	a.echoUser("hello")
	a.messages = append(a.messages, components.Message{Role: "assistant", Content: "hi"})
	a.persistTail()
	a.persistTail()

	entries := persistedEntries(t, a)
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2: %+v", len(entries), entries)
	}
}

func TestPersistWritesReasoningAndSystem(t *testing.T) {
	a := newPersistApp(t)
	a.echoUser("hello")
	a.messages = append(a.messages,
		components.Message{Role: "reasoning", Content: "chain of thought"},
		components.Message{Role: "system", Content: "notice"},
		components.Message{Role: "assistant", Content: "hi"},
	)
	a.persistTail()

	entries := persistedEntries(t, a)
	if len(entries) != 4 {
		t.Fatalf("entries = %d, want 4: %+v", len(entries), entries)
	}
	if entries[1].Type != "reasoning" || entries[1].Content != "chain of thought" {
		t.Fatalf("reasoning entry wrong: %+v", entries[1])
	}
	if entries[2].Type != "system" || entries[2].Content != "notice" {
		t.Fatalf("system entry wrong: %+v", entries[2])
	}
	if entries[3].Type != "assistant" || entries[3].Content != "hi" {
		t.Fatalf("assistant entry wrong: %+v", entries[3])
	}
}

func TestEchoUserNotDoubleWritten(t *testing.T) {
	a := newPersistApp(t)
	a.echoUser("hello")
	a.messages = append(a.messages, components.Message{Role: "assistant", Content: "hi"})
	a.persistTail()

	entries := persistedEntries(t, a)
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2: %+v", len(entries), entries)
	}
	if entries[0].Type != "user" || entries[0].Content != "hello" {
		t.Fatalf("user entry wrong: %+v", entries[0])
	}
}

func TestPersistTruncatesOversizeToolResult(t *testing.T) {
	a := newPersistApp(t)
	a.echoUser("big")
	big := strings.Repeat("x", maxToolResultBytes+100)
	a.messages = append(a.messages,
		components.Message{Role: "assistant", ToolCalls: []components.AgentToolCall{{ID: "c1", Name: "Bash", Args: `{"command":"yes"}`}}},
		components.Message{Role: "tool", ToolName: "Bash", ToolArgs: `{"command":"yes"}`, ToolCallID: "c1", Content: big, Status: "✓"},
	)
	a.persistTail()

	entries := persistedEntries(t, a)
	tool := entries[len(entries)-1]
	if len(tool.Content) != maxToolResultBytes {
		t.Fatalf("content len = %d, want %d", len(tool.Content), maxToolResultBytes)
	}
	if tool.Meta["truncated"] != true {
		t.Fatalf("truncated = %#v", tool.Meta["truncated"])
	}
	if tool.Meta["orig_len"] != float64(len(big)) {
		t.Fatalf("orig_len = %#v", tool.Meta["orig_len"])
	}
}

func TestPersistMultiPassNaturalExitReplies(t *testing.T) {
	a := newPersistApp(t)
	a.echoUser("plan it")

	// A plan pass loop runs several natural-exit passes: each reply is a
	// buffered assistant bubble separated by a pass-evaluator system line.
	// Both replies and the interleaved system line must reach the session
	// file, not just the trailing one.
	pass1 := components.Message{Role: "assistant"}
	pass1.AppendText("pass one plan")
	pass2 := components.Message{Role: "assistant"}
	pass2.AppendText("pass two plan")
	a.messages = append(a.messages,
		pass1,
		components.Message{Role: "system", Content: "plan evaluator: PLAN_PARTIAL (pass 1)"},
		pass2,
	)
	// Finalise the trailing reply the way EventDoneKind does.
	if last := a.trailingAssistant(); last >= 0 {
		a.messages[last].Materialise()
	}
	a.persistTail()

	entries := persistedEntries(t, a)
	if len(entries) != 4 {
		t.Fatalf("entries = %d, want 4: %+v", len(entries), entries)
	}
	if entries[1].Type != "assistant" || entries[1].Content != "pass one plan" {
		t.Fatalf("pass 1 entry wrong: %+v", entries[1])
	}
	if entries[2].Type != "system" || entries[2].Content != "plan evaluator: PLAN_PARTIAL (pass 1)" {
		t.Fatalf("system entry wrong: %+v", entries[2])
	}
	if entries[3].Type != "assistant" || entries[3].Content != "pass two plan" {
		t.Fatalf("pass 2 entry wrong: %+v", entries[3])
	}
}

func TestPersistErrorPathWritesStreamedReply(t *testing.T) {
	a := newPersistApp(t)
	a.echoUser("plan it")

	// Stream a reply, then fail the turn the way a terminal agent error does.
	// The streamed reply must still be written so /resume has the model output.
	a.handleAgentEvent(agentEventMsg{Kind: agent.EventTextKind, Text: "partial plan"})
	a.handleAgentEvent(agentEventMsg{Kind: agent.EventErrorKind, Err: errors.New("boom")})

	entries := persistedEntries(t, a)
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3: %+v", len(entries), entries)
	}
	if entries[1].Type != "assistant" || entries[1].Content != "partial plan" {
		t.Fatalf("assistant entry wrong: %+v", entries[1])
	}
	if entries[2].Type != "system" || entries[2].Content != "agent error: boom" {
		t.Fatalf("system entry wrong: %+v", entries[2])
	}
}

func TestPersistWritesRolemanagerRows(t *testing.T) {
	a := newPersistApp(t)
	a.echoUser("hello")
	a.messages = append(a.messages,
		components.Message{
			Role:  "rolemanager",
			Level: rolemanager.LevelSecurity,
			RM: rolemanager.Description{
				Summary: "Checked what the file Signet read returned",
				Outcome: "clean",
				Tone:    rolemanager.ToneClear,
				Levels:  rolemanager.LevelSecurity,
			},
		},
		components.Message{Role: "assistant", Content: "hi"},
	)
	a.persistTail()

	entries := persistedEntries(t, a)
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3: %+v", len(entries), entries)
	}
	rm := entries[1]
	if rm.Type != "rolemanager" || rm.Content != "Checked what the file Signet read returned — clean" {
		t.Fatalf("rolemanager entry wrong: %+v", rm)
	}
	if rm.Meta["summary"] != "Checked what the file Signet read returned" {
		t.Fatalf("summary meta = %#v", rm.Meta["summary"])
	}
	if rm.Meta["outcome"] != "clean" ||
		rm.Meta["tone"] != float64(rolemanager.ToneClear) ||
		rm.Meta["level"] != float64(rolemanager.LevelSecurity) {
		t.Fatalf("rolemanager meta = %#v", rm.Meta)
	}
	if entries[2].Type != "assistant" || entries[2].Content != "hi" {
		t.Fatalf("assistant entry wrong: %+v", entries[2])
	}
}

func TestPersistAbortedTrailingToolCallNotWritten(t *testing.T) {
	a := newPersistApp(t)
	a.echoUser("run it")
	// A trailing assistant with tool calls whose results never landed must
	// not be written with unpaired tool_calls.
	a.messages = append(a.messages, components.Message{
		Role:      "assistant",
		Content:   "running a command",
		ToolCalls: []components.AgentToolCall{{ID: "c1", Name: "Bash", Args: `{"command":"ls"}`}},
	})
	a.persistTail()

	entries := persistedEntries(t, a)
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1 (user only): %+v", len(entries), entries)
	}
}

func TestPersistTwoRoundToolTurn(t *testing.T) {
	a := newPersistApp(t)
	a.echoUser("two tools")
	a.messages = append(a.messages,
		components.Message{Role: "assistant", ToolCalls: []components.AgentToolCall{
			{ID: "c1", Name: "Read", Args: `{"path":"a"}`},
			{ID: "c2", Name: "Read", Args: `{"path":"b"}`},
		}},
		components.Message{Role: "tool", ToolName: "Read", ToolArgs: `{"path":"a"}`, ToolCallID: "c1", Content: "a-result", Status: "✓"},
		components.Message{Role: "tool", ToolName: "Read", ToolArgs: `{"path":"b"}`, ToolCallID: "c2", Content: "b-result", Status: "✓"},
		components.Message{Role: "assistant", Content: "final reply"},
	)
	a.persistTail()

	entries := persistedEntries(t, a)
	// user, assistant(tool_calls), tool c1, tool c2, assistant(final).
	if len(entries) != 5 {
		t.Fatalf("entries = %d, want 5: %+v", len(entries), entries)
	}
	if entries[1].Type != "assistant" || entries[2].Type != "tool" || entries[3].Type != "tool" || entries[4].Type != "assistant" {
		t.Fatalf("types = %q/%q/%q/%q", entries[1].Type, entries[2].Type, entries[3].Type, entries[4].Type)
	}
	if entries[2].Meta["tool_call_id"] != "c1" || entries[3].Meta["tool_call_id"] != "c2" {
		t.Fatalf("tool ids wrong: %#v %#v", entries[2].Meta, entries[3].Meta)
	}
	if entries[4].Content != "final reply" {
		t.Fatalf("final assistant content = %q", entries[4].Content)
	}
}

func TestPersistSkipsOrphanedEmptyAssistant(t *testing.T) {
	a := newPersistApp(t)
	a.echoUser("hello")
	// The send-time empty assistant bubble is left behind streamed reasoning
	// when a reasoning model thinks before answering. It must not block the
	// reasoning and assistant that follow from reaching the session file.
	a.messages = append(a.messages,
		components.Message{Role: "assistant"},
		components.Message{Role: "reasoning", Content: "thinking"},
		components.Message{Role: "assistant", Content: "hi"},
	)
	a.persistTail()

	entries := persistedEntries(t, a)
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3 (user, reasoning, assistant): %+v", len(entries), entries)
	}
	if entries[1].Type != "reasoning" || entries[2].Type != "assistant" {
		t.Fatalf("types = %q/%q", entries[1].Type, entries[2].Type)
	}
}

func TestPersistTrailingEmptyAssistantNotWritten(t *testing.T) {
	a := newPersistApp(t)
	a.echoUser("hello")
	// A trailing empty assistant is the in-flight turn bubble; it must stop
	// the scan rather than be persisted as an empty frame.
	a.messages = append(a.messages, components.Message{Role: "assistant"})
	a.persistTail()

	entries := persistedEntries(t, a)
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1 (user only): %+v", len(entries), entries)
	}
}

func TestPersistTrailingReasoningAfterMaterialise(t *testing.T) {
	a := newPersistApp(t)
	a.echoUser("hello")
	r := components.Message{Role: "reasoning"}
	r.AppendText("thinking")
	a.messages = append(a.messages, r)
	// EventDoneKind materialises the trailing reasoning before persistTail.
	if last := a.trailingReasoning(); last >= 0 {
		a.messages[last].Materialise()
	}
	a.persistTail()

	entries := persistedEntries(t, a)
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2: %+v", len(entries), entries)
	}
	if entries[1].Type != "reasoning" || entries[1].Content != "thinking" {
		t.Fatalf("reasoning entry wrong: %+v", entries[1])
	}
}

func TestEchoUserFlushesTrailingSystemRow(t *testing.T) {
	a := newPersistApp(t)
	a.addSystem("trailing notice")
	// echoUser must flush the pending notice before advancing the cursor past
	// the new user turn, so the notice is not skipped.
	a.echoUser("hello")

	entries := persistedEntries(t, a)
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2 (system, user): %+v", len(entries), entries)
	}
	if entries[0].Type != "system" || entries[0].Content != "trailing notice" {
		t.Fatalf("system entry wrong: %+v", entries[0])
	}
	if entries[1].Type != "user" || entries[1].Content != "hello" {
		t.Fatalf("user entry wrong: %+v", entries[1])
	}
}

func TestCurrentAssistantBubbleAttachesConsecutiveToolCalls(t *testing.T) {
	a := newPersistApp(t)
	a.echoUser("run two")
	// The send-time empty assistant bubble exists, then two tool starts land
	// back to back (a concurrent read-only group). Both calls must attach to
	// the same assistant bubble, not only the first.
	a.handleAgentEvent(agentEventMsg{Kind: agent.EventToolStartKind, Tool: &rolemanager.ToolCall{ID: "c1", Name: "Read", Args: map[string]any{"path": "a"}}})
	a.handleAgentEvent(agentEventMsg{Kind: agent.EventToolStartKind, Tool: &rolemanager.ToolCall{ID: "c2", Name: "Read", Args: map[string]any{"path": "b"}}})

	// The assistant bubble is the only assistant message in the transcript.
	var bubble *components.Message
	for i := range a.messages {
		if a.messages[i].Role == "assistant" {
			bubble = &a.messages[i]
			break
		}
	}
	if bubble == nil || len(bubble.ToolCalls) != 2 {
		t.Fatalf("assistant bubble = %+v", bubble)
	}
	if bubble.ToolCalls[0].ID != "c1" || bubble.ToolCalls[1].ID != "c2" {
		t.Fatalf("attached calls = %+v", bubble.ToolCalls)
	}
}

// TestPersistInterleavedGoalTurn is the goal-mode shape that used to persist
// nothing: reasoning, system and role-manager rows land between an assistant
// and its tool rows. Every row must reach disk, in order.
func TestPersistInterleavedGoalTurn(t *testing.T) {
	a := newPersistApp(t)
	a.echoUser("fix it")
	a.messages = append(a.messages,
		components.Message{Role: "assistant", Content: "working", ToolCalls: []components.AgentToolCall{
			{ID: "c1", Name: "Read", Args: `{"path":"a"}`},
			{ID: "c2", Name: "Write", Args: `{"path":"a"}`},
		}},
		components.Message{Role: "tool", ToolName: "Read", ToolCallID: "c1", Content: "a", Status: "✓"},
		components.Message{Role: "reasoning", Content: "now write"},
		components.Message{Role: "system", Content: "goal evaluator: partial (pass 1)"},
		components.Message{Role: "rolemanager", RM: rolemanager.Description{Summary: "Checked the file", Outcome: "clean"}},
		components.Message{Role: "tool", ToolName: "Write", ToolCallID: "c2", Content: "wrote a", Status: "✓"},
	)
	a.persistTail()

	entries := persistedEntries(t, a)
	var types []string
	for _, e := range entries {
		types = append(types, e.Type)
	}
	want := "user assistant tool reasoning system rolemanager tool"
	if got := strings.Join(types, " "); got != want {
		t.Fatalf("persisted %q, want %q", got, want)
	}
	if calls, _ := entries[1].Meta["tool_calls"].([]any); len(calls) != 2 {
		t.Fatalf("assistant tool_calls = %#v, want both calls", entries[1].Meta["tool_calls"])
	}
}

// TestPersistHoldsInFlightBubble: while a turn runs, its assistant bubble can
// still gain calls, so it is held even when every current call is answered;
// rows before it still persist.
func TestPersistHoldsInFlightBubble(t *testing.T) {
	a := newPersistApp(t)
	a.echoUser("fix it")
	a.messages = append(a.messages,
		components.Message{Role: "rolemanager", RM: rolemanager.Description{Summary: "Admitted", Outcome: "clean"}},
		components.Message{Role: "assistant", ToolCalls: []components.AgentToolCall{{ID: "c1", Name: "Read"}}},
		components.Message{Role: "tool", ToolName: "Read", ToolCallID: "c1", Content: "a", Status: "✓"},
	)
	a.cancel = func() {}
	a.persistTail()
	if got := len(persistedEntries(t, a)); got != 2 {
		t.Fatalf("in flight: entries = %d, want 2 (user, rolemanager)", got)
	}

	a.cancel = nil
	a.persistTail()
	if got := len(persistedEntries(t, a)); got != 4 {
		t.Fatalf("after the turn: entries = %d, want 4", got)
	}
}

// TestEchoUserForceFlushesAbortedTurn: an aborted call never gets a result.
// The next user turn must write what it can — the assistant without the
// unanswered call — and everything after, instead of skipping the rows.
func TestEchoUserForceFlushesAbortedTurn(t *testing.T) {
	a := newPersistApp(t)
	a.echoUser("run it")
	a.messages = append(a.messages,
		components.Message{Role: "assistant", Content: "running", ToolCalls: []components.AgentToolCall{
			{ID: "c1", Name: "Read"}, {ID: "c2", Name: "Bash"},
		}},
		components.Message{Role: "tool", ToolName: "Read", ToolCallID: "c1", Content: "a", Status: "✓"},
		components.Message{Role: "tool", ToolName: "Bash", ToolCallID: "c2"},
		components.Message{Role: "system", Content: "request cancelled"},
	)
	a.persistTail()
	if got := len(persistedEntries(t, a)); got != 1 {
		t.Fatalf("before the next turn: entries = %d, want 1", got)
	}

	a.echoUser("try again")
	entries := persistedEntries(t, a)
	var types []string
	for _, e := range entries {
		types = append(types, e.Type)
	}
	if got, want := strings.Join(types, " "), "user assistant tool system user"; got != want {
		t.Fatalf("persisted %q, want %q", got, want)
	}
	calls, _ := entries[1].Meta["tool_calls"].([]any)
	if len(calls) != 1 || calls[0].(map[string]any)["id"] != "c1" {
		t.Fatalf("assistant tool_calls = %#v, want only the answered c1", entries[1].Meta["tool_calls"])
	}
}

// TestEchoUserDuringRunningTurnKeepsOrder: steering mid-turn must not jump
// the cursor past the running turn's rows; the user row is written in order
// once they settle.
func TestEchoUserDuringRunningTurnKeepsOrder(t *testing.T) {
	a := newPersistApp(t)
	a.echoUser("fix it")
	a.messages = append(a.messages,
		components.Message{Role: "assistant", ToolCalls: []components.AgentToolCall{{ID: "c1", Name: "Read"}}},
		components.Message{Role: "tool", ToolName: "Read", ToolCallID: "c1"},
	)
	a.cancel = func() {}
	a.echoUser("also update the docs")
	a.messages[2].Content, a.messages[2].Status = "a", "✓"
	a.cancel = nil
	a.persistTail()

	entries := persistedEntries(t, a)
	var types []string
	for _, e := range entries {
		types = append(types, e.Type)
	}
	if got, want := strings.Join(types, " "), "user assistant tool user"; got != want {
		t.Fatalf("persisted %q, want %q", got, want)
	}
}

func TestTruncateUTF8KeepsRunes(t *testing.T) {
	s := "ab—cd" // em-dash is bytes 2..4
	if got := truncateUTF8(s, 3); got != "ab" {
		t.Fatalf("truncateUTF8 = %q, want %q", got, "ab")
	}
	if got := truncateUTF8(s, 100); got != s {
		t.Fatalf("short input changed: %q", got)
	}
}

// TestInterleavedGoalTurnRehydratesWithoutDrops closes the loop: the
// interleaved layout persisted above must resume with every call paired.
func TestInterleavedGoalTurnRehydratesWithoutDrops(t *testing.T) {
	a := newPersistApp(t)
	a.echoUser("fix it")
	a.messages = append(a.messages,
		components.Message{Role: "assistant", Content: "working", ToolCalls: []components.AgentToolCall{
			{ID: "c1", Name: "Read", Args: `{"path":"a"}`},
			{ID: "c2", Name: "Write", Args: `{"path":"a"}`},
		}},
		components.Message{Role: "tool", ToolName: "Read", ToolCallID: "c1", Content: "a", Status: "✓"},
		components.Message{Role: "reasoning", Content: "now write"},
		components.Message{Role: "system", Content: "goal evaluator: partial (pass 1)"},
		components.Message{Role: "tool", ToolName: "Write", ToolCallID: "c2", Content: "wrote a", Status: "✓"},
		components.Message{Role: "assistant", Content: "done"},
	)
	a.persistTail()

	r := rehydrateSession(persistedEntries(t, a))
	if r.Dropped != 0 {
		t.Fatalf("rehydrate dropped %d tool rows from an interleaved goal turn", r.Dropped)
	}
	tools := 0
	for _, m := range r.Messages {
		if m.Role == "tool" {
			tools++
		}
	}
	if tools != 2 {
		t.Fatalf("rehydrated %d tool rows, want 2", tools)
	}
}
