package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tui/components"
)

func sideApp(t *testing.T, reply string) *App {
	t.Helper()
	// The usage ledger writes its file from a goroutine after the call, so the
	// directory is removed with a retry rather than by t.TempDir.
	home, err := os.MkdirTemp("", "belai-side-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for i := 0; i < 40; i++ {
			if os.RemoveAll(home) == nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
	t.Setenv("BELAI_HOME", home)
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	srv := newAgentSSEServer(t, reply)
	t.Cleanup(srv.Close)
	src := &fakeCredentialSource{vals: map[string]string{"openai:api_key": "sk-test"}}
	cfg, status := run.Prepare("gpt-5", "openai", src)
	if !status.Configured {
		t.Fatal("not configured")
	}
	cfg.BaseURL = srv.URL
	a := New(Options{Client: srv.Client(), Provider: "openai", Model: "gpt-5", Workdir: t.TempDir()})
	a.cfg = cfg
	a.status = status
	a.messages = append(a.messages,
		components.Message{Role: "user", Content: "fix the parser"},
		components.Message{Role: "assistant", Content: "I changed parse.go"},
	)
	return a
}

func askSide(t *testing.T, a *App, arg string) {
	t.Helper()
	cmd := a.handleCommand("/btw " + arg)
	if cmd == nil {
		t.Fatalf("/btw %q started nothing; last row: %q", arg, lastSystem(a))
	}
	msg, ok := cmd().(sideAnswerMsg)
	if !ok {
		t.Fatalf("unexpected message %T", cmd())
	}
	a.Update(msg)
}

func sessionFileBytes(t *testing.T, a *App) []byte {
	t.Helper()
	var out []byte
	_ = filepath.WalkDir(filepath.Join(os.Getenv("BELAI_HOME"), "sessions"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			out = append(out, p...)
			out = append(out, b...)
		}
		return nil
	})
	return out
}

func TestBtwLeavesHistoryAndSessionFileUnchanged(t *testing.T) {
	a := sideApp(t, "It parses lines.")
	beforeTurns := a.buildTurns()
	beforeTx := a.transcriptMessages()
	// The fixture's own two messages are written first, so the comparison
	// below sees only what /btw adds.
	a.persistTail()
	beforeFile := sessionFileBytes(t, a)

	askSide(t, a, "what does parse.go do?")

	if got := a.buildTurns(); len(got) != len(beforeTurns) {
		t.Fatalf("history changed: %d -> %d turns", len(beforeTurns), len(got))
	}
	if got := a.transcriptMessages(); len(got) != len(beforeTx)+2 {
		// The two harness rows are system rows, which no serialiser reads.
		t.Fatalf("transcript = %d messages", len(got))
	}
	for _, m := range a.transcriptMessages() {
		if m.Role != "system" && strings.Contains(m.Content, "It parses lines.") {
			t.Fatal("the answer is in a conversation message")
		}
	}
	var answer *components.Message
	for i := range a.messages {
		if a.messages[i].Side && strings.Contains(a.messages[i].Text(), "It parses lines.") {
			answer = &a.messages[i]
		}
	}
	if answer == nil || !answer.Ephemeral || !strings.Contains(answer.Text(), "not in history") {
		t.Fatalf("no ephemeral side row with the answer: %+v", a.messages)
	}
	if !neverPersisted(*answer) {
		t.Fatal("the answer row can be persisted")
	}
	a.persistTail()
	if after := sessionFileBytes(t, a); string(after) != string(beforeFile) {
		t.Fatalf("the session file changed:\n%s", after)
	}
	if a.side.busy {
		t.Fatal("busy flag left set")
	}
}

func TestBtwRefusals(t *testing.T) {
	a := sideApp(t, "x")
	if cmd := a.handleCommand("/btw"); cmd != nil || !strings.Contains(lastSystem(a), "usage: /btw") {
		t.Fatalf("empty argument: cmd=%v row=%q", cmd != nil, lastSystem(a))
	}
	a.settings.Side = &config.SideSettings{Enabled: bp(false)}
	if cmd := a.handleCommand("/btw hello"); cmd != nil || !strings.Contains(lastSystem(a), "side.enabled") {
		t.Fatalf("disabled: row=%q", lastSystem(a))
	}
	a.settings.Side = nil
	a.side.busy = true
	if cmd := a.handleCommand("/btw hello"); cmd != nil || !strings.Contains(lastSystem(a), "already running") {
		t.Fatalf("concurrent: row=%q", lastSystem(a))
	}
}

func TestBtwWhileATurnRunsIsAllowed(t *testing.T) {
	a := sideApp(t, "ok")
	a.cancel = func() {}
	if cmd := a.handleCommand("/btw hello"); cmd == nil {
		t.Fatalf("a running turn blocked /btw: %q", lastSystem(a))
	}
}

func TestBtwFailureShowsAFixedLine(t *testing.T) {
	a := sideApp(t, "   ")
	askSide(t, a, "hello")
	if got := lastSystem(a); got != sideFailed {
		t.Fatalf("row = %q", got)
	}
	if a.side.busy {
		t.Fatal("busy flag left set after a failure")
	}
}

func TestBtwAnswerForAnotherSessionIsDropped(t *testing.T) {
	a := sideApp(t, "ok")
	a.side.busy = true
	n := len(a.messages)
	a.handleSideAnswer(sideAnswerMsg{sessionID: "someone-else", question: "q", reply: "late"})
	if len(a.messages) != n || a.side.busy {
		t.Fatalf("a stale answer was shown (%d -> %d rows, busy=%v)", n, len(a.messages), a.side.busy)
	}
}

func TestBtwClearHidesTheRows(t *testing.T) {
	a := sideApp(t, "ok")
	askSide(t, a, "hello")
	shown, _ := a.filteredMessagesIdx()
	hasSide := func(ms []components.Message) bool {
		for _, m := range ms {
			if m.Side && !m.Dismissed {
				return true
			}
		}
		return false
	}
	if !hasSide(shown) {
		t.Fatal("the answer is not shown")
	}
	a.handleCommand("/btw clear")
	shown, _ = a.filteredMessagesIdx()
	for _, m := range shown {
		if m.Side {
			t.Fatalf("a side row is still shown: %q", m.Text())
		}
	}
	a.handleCommand("/btw clear")
	if got := lastSystem(a); !strings.Contains(got, "no side answers") {
		t.Fatalf("second clear: %q", got)
	}
}

func TestSideTailKeepsOnlyConversationText(t *testing.T) {
	a := sideApp(t, "ok")
	a.messages = []components.Message{
		{Role: "user", Content: "one"},
		{Role: "tool", Content: "SECRET tool output", ToolName: "Bash"},
		{Role: "assistant", Content: "two"},
		{Role: "system", Content: "harness notice"},
		{Role: "assistant", Content: "subagent chatter", SubagentID: "s1"},
		{Role: "assistant", Content: "failed attempt", Partial: true},
		{Role: "system", Content: "side answer", Side: true, Ephemeral: true},
		{Role: "user", Content: "three"},
	}
	got := a.sideTail(2)
	if got != "[assistant]\ntwo\n\n[user]\nthree\n" {
		t.Fatalf("tail = %q", got)
	}
	if a.sideTail(0) != "" {
		t.Fatal("history_turns 0 still reads the conversation")
	}
	all := a.sideContext(20)
	for _, bad := range []string{"SECRET", "harness notice", "subagent chatter", "failed attempt", "side answer"} {
		if strings.Contains(all, bad) {
			t.Errorf("context carries %q:\n%s", bad, all)
		}
	}
}

func TestSideTailIsByteBounded(t *testing.T) {
	a := sideApp(t, "ok")
	a.messages = nil
	for i := 0; i < 20; i++ {
		a.messages = append(a.messages, components.Message{Role: "user", Content: strings.Repeat("x", 1400)})
	}
	if got := a.sideTail(20); len(got) > sideTailBytes {
		t.Fatalf("tail is %d bytes, cap %d", len(got), sideTailBytes)
	}
}

func TestSideRowsAreNeverSent(t *testing.T) {
	a := sideApp(t, "ok")
	a.addSideRow("side answer, not in history\nsecret-marker")
	for _, turn := range a.buildTurns() {
		if strings.Contains(turn.Content, "secret-marker") {
			t.Fatal("a side row reached the provider turns")
		}
	}
}
