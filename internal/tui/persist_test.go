package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/agent"
	"github.com/vulnetix/signet/internal/session"
	"github.com/vulnetix/signet/internal/tui/components"
)

// newPersistApp builds an App with an isolated store and a workdir.
func newPersistApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("SIGNET_HOME", t.TempDir())
	return New(Options{Provider: "openai", Model: "gpt-5", Workdir: t.TempDir()})
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

func TestPersistSkipsReasoningAndSystem(t *testing.T) {
	a := newPersistApp(t)
	a.echoUser("hello")
	a.messages = append(a.messages,
		components.Message{Role: "reasoning", Content: "chain of thought"},
		components.Message{Role: "system", Content: "notice"},
		components.Message{Role: "assistant", Content: "hi"},
	)
	a.persistTail()

	entries := persistedEntries(t, a)
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2: %+v", len(entries), entries)
	}
	if entries[1].Type != "assistant" || entries[1].Content != "hi" {
		t.Fatalf("wrong entry: %+v", entries[1])
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
	// Both replies must reach the session file, not just the trailing one.
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
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3: %+v", len(entries), entries)
	}
	if entries[1].Type != "assistant" || entries[1].Content != "pass one plan" {
		t.Fatalf("pass 1 entry wrong: %+v", entries[1])
	}
	if entries[2].Type != "assistant" || entries[2].Content != "pass two plan" {
		t.Fatalf("pass 2 entry wrong: %+v", entries[2])
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
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2: %+v", len(entries), entries)
	}
	if entries[1].Type != "assistant" || entries[1].Content != "partial plan" {
		t.Fatalf("assistant entry wrong: %+v", entries[1])
	}
}

func TestPersistSkipsRolemanagerRows(t *testing.T) {
	a := newPersistApp(t)
	a.echoUser("hello")
	a.messages = append(a.messages,
		components.Message{Role: "rolemanager", Content: "checked what Read returned"},
		components.Message{Role: "assistant", Content: "hi"},
	)
	a.persistTail()

	entries := persistedEntries(t, a)
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2: %+v", len(entries), entries)
	}
	if entries[1].Type != "assistant" || entries[1].Content != "hi" {
		t.Fatalf("wrong entry: %+v", entries[1])
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
