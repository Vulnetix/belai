package turnlog

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/session"
)

func newLog(t *testing.T) (*Log, func() []session.Entry) {
	t.Helper()
	st := session.NewStoreAt(t.TempDir())
	key := session.Key("proj-1")
	w, err := session.NewWriter(st, key, session.MustID(), session.Meta{Cwd: "/src"})
	if err != nil {
		t.Fatal(err)
	}
	return New(w), func() []session.Entry {
		entries, err := st.ReadFrom(key, w.ID())
		if err != nil {
			t.Fatal(err)
		}
		return entries
	}
}

// A nil writer makes every method a no-op, so a caller whose transcript
// failed to open keeps working.
func TestNilWriterIsANoOp(t *testing.T) {
	l := New(nil)
	if l.Writer() != nil || l.User("x", nil) != "" {
		t.Fatal("nil log wrote")
	}
	l.System("x")
	l.Observe(agent.Event{Kind: agent.EventTextKind, Text: "x"})
	l.TurnStarted(map[string]any{"mode": "goal"})
	l.TurnEnded("ended", nil)
	l.Flush()
}

func TestTurnIsRecorded(t *testing.T) {
	l, read := newLog(t)
	l.TurnStarted(map[string]any{"mode": "goal", "model": "m"})
	if id := l.User("fix it", map[string]any{"source": "rc"}); id == "" {
		t.Fatal("user line not written")
	}
	l.Observe(agent.Event{Kind: agent.EventTextKind, Text: "Look"})
	l.Observe(agent.Event{Kind: agent.EventTextKind, Text: "ing."})
	l.Observe(agent.Event{Kind: agent.EventToolStartKind, Tool: &rolemanager.ToolCall{ID: "c1", Name: "Read", Args: map[string]any{"path": "a.go"}}})
	l.Observe(agent.Event{Kind: agent.EventToolStartKind, Tool: &rolemanager.ToolCall{ID: "c2", Name: "Grep", RawArgs: `{"pattern":"x"}`}})
	l.Observe(agent.Event{Kind: agent.EventToolStartKind}) // no call: ignored
	l.Observe(agent.Event{Kind: agent.EventToolResultKind, ToolCallID: "c1", ToolName: "Read", ToolArgs: `{"path":"a.go"}`, ToolResult: "package a"})
	l.Observe(agent.Event{Kind: agent.EventWarningKind, Warning: "slow"})
	l.Observe(agent.Event{Kind: agent.EventWarningKind}) // empty: ignored
	l.Observe(agent.Event{Kind: agent.EventTextKind, Text: "Done."})
	l.System("released")
	l.TurnEnded("ended", map[string]any{"passes": 1})

	var kinds []string
	for _, e := range read() {
		kinds = append(kinds, e.Type)
	}
	want := []string{"session_meta", "turn_state", "user", "assistant", "tool", "system", "system", "assistant", "turn_state"}
	if len(kinds) != len(want) {
		t.Fatalf("entries = %v", kinds)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("entries = %v, want %v", kinds, want)
		}
	}

	e := read()
	if e[1].Meta["state"] != "started" || e[1].Meta["mode"] != "goal" || e[1].Meta["turn_id"] == "" {
		t.Fatalf("start = %+v", e[1].Meta)
	}
	// The text and calls before the result go out as one assistant line.
	if e[3].Content != "Looking." {
		t.Fatalf("assistant = %q", e[3].Content)
	}
	end := e[8].Meta
	if end["state"] != "ended" || end["turn_id"] != e[1].Meta["turn_id"] || end["passes"] == nil || end["duration_ms"] == nil {
		t.Fatalf("end = %+v", end)
	}
	if e[7].Content != "Done." {
		t.Fatalf("closing text = %q", e[7].Content)
	}
}

// An end without a start writes nothing but still flushes the text.
func TestTurnEndedWithoutStart(t *testing.T) {
	l, read := newLog(t)
	l.Observe(agent.Event{Kind: agent.EventTextKind, Text: "hi"})
	l.TurnEnded("error", nil)
	e := read()
	if len(e) != 2 || e[1].Type != "assistant" {
		t.Fatalf("entries = %+v", e)
	}
	// A blank flush writes nothing.
	l.Observe(agent.Event{Kind: agent.EventTextKind, Text: "  "})
	l.Flush()
	if len(read()) != 2 {
		t.Fatal("blank text written")
	}
}

// A shell entry is the TUI's: type and role "shell", the command, its id and
// its status in meta, the output as content, cut at a rune boundary when long.
func TestShellEntryShape(t *testing.T) {
	l, read := newLog(t)
	if id := l.Shell("M go.mod", map[string]any{"command": "git status --short", "shell_id": "c1", "status": "✓"}); id == "" {
		t.Fatal("shell entry not written")
	}
	long := strings.Repeat("é", maxShellBytes) // two bytes per rune
	l.Shell(long, map[string]any{"command": "cat big", "shell_id": "c2", "status": "✓"})

	var shells []session.Entry
	for _, e := range read() {
		if e.Type == ShellEntry {
			shells = append(shells, e)
		}
	}
	if len(shells) != 2 {
		t.Fatalf("shell entries = %d, want 2", len(shells))
	}
	if shells[0].Role != "shell" || shells[0].Content != "M go.mod" || shells[0].Meta["shell_id"] != "c1" {
		t.Fatalf("entry = %+v", shells[0])
	}
	if len(shells[1].Content) > maxShellBytes || !utf8.ValidString(shells[1].Content) {
		t.Fatalf("long entry has %d bytes, valid=%v", len(shells[1].Content), utf8.ValidString(shells[1].Content))
	}
	if shells[1].Meta["truncated"] != true {
		t.Fatalf("meta = %v, want truncated", shells[1].Meta)
	}
}
