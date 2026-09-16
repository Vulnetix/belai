package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/tui/components"
)

func TestShellPlanModeRejectsRmRf(t *testing.T) {
	a := New(Options{})
	a.mode = "plan"
	cmd := a.handleShell("!rm -rf /")
	if cmd != nil {
		t.Fatalf("plan mode should refuse rm -rf without executing")
	}
	found := false
	for _, m := range a.messages {
		if m.Role == "system" && strings.Contains(m.Content, "not allowed") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected refusal message, got %v", a.messages)
	}
}

func TestBareExclamationIsNoOp(t *testing.T) {
	a := New(Options{})
	if a.handleShell("!") != nil {
		t.Fatalf("bare ! should be a no-op")
	}
}

// TestShellEchoRunsOnItsOwnToolRow: `!cmd` is rendered as a Bash tool row, so
// it inherits the live tail, the tail-anchored preview and ctrl+o rather than
// being a second, divergent presentation of a command's output.
func TestShellEchoRunsOnItsOwnToolRow(t *testing.T) {
	a := New(Options{})
	a.mode = "agent"
	cmd := a.handleShell("!echo hello-signet")
	if cmd == nil {
		t.Fatalf("expected a command")
	}

	row := lastToolRow(t, a)
	if row.ToolName != "Bash" || row.StartedAt.IsZero() {
		t.Fatalf("expected a running Bash row, got %+v", row)
	}
	if !strings.Contains(row.ToolArgs, "echo hello-signet") {
		t.Fatalf("row does not carry the command: %q", row.ToolArgs)
	}

	// handleShell batches the execution with the progress watcher; run the
	// batch and pick out the completion. Classification needs a configured
	// provider, which this App does not have, so only the routing is asserted
	// here — the body path is covered below.
	done, ok := runBatchFor[shellDoneMsg](t, cmd)
	if !ok {
		t.Fatal("no shellDoneMsg produced")
	}
	if done.callID != row.ToolCallID {
		t.Fatalf("result keyed %q, row is %q", done.callID, row.ToolCallID)
	}
}

// TestShellResultReplacesTheLiveTail: once the real output is in, the partial
// view of it must go, and it must land on the row rather than a system notice.
func TestShellResultReplacesTheLiveTail(t *testing.T) {
	a := New(Options{})
	a.mode = "agent"
	if cmd := a.handleShell("!seq 5"); cmd == nil {
		t.Fatal("expected a command")
	}
	row := lastToolRow(t, a)
	row.AppendProgress("1\n2")
	if !row.HasProgress() {
		t.Fatal("progress did not attach to the row")
	}

	a.setShellResult(row.ToolCallID, "1\n2\n3\n4\n5", "✓")

	row = lastToolRow(t, a)
	if row.Text() != "1\n2\n3\n4\n5" {
		t.Fatalf("row content = %q", row.Text())
	}
	if row.HasProgress() {
		t.Fatal("live tail should be cleared once the result lands")
	}
	for _, m := range a.messages {
		if m.Role == "system" && strings.Contains(m.Content, "1\n2") {
			t.Fatal("output should not also be echoed as a system notice")
		}
	}
}

// TestShellFailureLandsOnTheRow keeps a failed command's report in the same
// place as a successful one.
func TestShellFailureLandsOnTheRow(t *testing.T) {
	a := New(Options{})
	a.mode = "agent"
	if cmd := a.handleShell("!echo x"); cmd == nil {
		t.Fatal("expected a command")
	}
	row := lastToolRow(t, a)

	a.handleShellDone(shellDoneMsg{command: "echo x", callID: row.ToolCallID, err: errBoom})
	row = lastToolRow(t, a)
	if !strings.Contains(row.Text(), "boom") {
		t.Fatalf("failure not reported on the row: %q", row.Text())
	}
	if row.Status != "✗" {
		t.Fatalf("status = %q, want ✗", row.Status)
	}
}

var errBoom = errors.New("boom")

func lastToolRow(t *testing.T, a *App) *components.Message {
	t.Helper()
	for i := len(a.messages) - 1; i >= 0; i-- {
		if a.messages[i].Role == "tool" {
			return &a.messages[i]
		}
	}
	t.Fatal("no tool row in the transcript")
	return nil
}

// runBatchFor runs a tea.Cmd that may be a batch and returns the first message
// of type T it produces.
func runBatchFor[T tea.Msg](t *testing.T, cmd tea.Cmd) (T, bool) {
	t.Helper()
	var zero T
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		if v, ok := msg.(T); ok {
			return v, true
		}
		return zero, false
	}
	for _, c := range batch {
		if v, ok := c().(T); ok {
			return v, true
		}
	}
	return zero, false
}
