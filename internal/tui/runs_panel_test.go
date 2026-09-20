package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/activity"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/tui/components"
)

func TestRunsPanelHeightClosedZero(t *testing.T) {
	a := New(Options{})
	if h := a.runsPanelHeight(); h != 0 {
		t.Fatalf("closed panel height = %d, want 0", h)
	}
}

func TestF9OpensRunsPanelActivityFromPanelFile(t *testing.T) {
	a := New(Options{})
	a.height = 24
	a.Update(tea.KeyMsg{Type: tea.KeyF9})
	if !a.runsOpen || !a.runsFocus || a.runsTab != tabActivity {
		t.Fatal("f9 must open and focus the activity tab")
	}
}

func TestEscUnfocusesPanel(t *testing.T) {
	a := New(Options{})
	a.runsOpen = true
	a.runsFocus = true
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEsc})
	if a.runsFocus || !a.runsOpen {
		t.Fatal("esc must unfocus while leaving the panel open")
	}
}

func TestTabSwitchesRunsTabs(t *testing.T) {
	a := New(Options{})
	a.runsOpen = true
	a.runsFocus = true
	a.runsTab = tabActivity
	a.handleRunsPanelKey(tea.KeyMsg{Type: tea.KeyTab})
	if a.runsTab != tabSubagents {
		t.Fatalf("tab switch failed: got %d", a.runsTab)
	}
}

func TestFlushPendingActivitySendsWhenIdle(t *testing.T) {
	a := New(Options{})
	a.pendingActivitySends = []activitySend{
		{label: "a", atts: []run.Attachment{{Kind: "shell", Label: "a", Body: "x"}}},
		{label: "b", atts: []run.Attachment{{Kind: "shell", Label: "b", Body: "y"}}},
	}
	if cmd := a.flushPendingActivitySends(); cmd == nil {
		t.Fatal("idle flush must produce a send command")
	}
	if len(a.pendingActivitySends) != 0 {
		t.Fatalf("flush must drain pending sends, got %d", len(a.pendingActivitySends))
	}
}

func TestActivityKillRoutingInPanel(t *testing.T) {
	a := New(Options{})
	a.activity = activity.NewRegistry()
	h := a.activity.Add(activity.Activity{Kind: activity.KindShell, Label: "!x", State: activity.StateRunning}, func() {})
	a.runsOpen = true
	a.runsFocus = true
	a.runsTab = tabActivity
	a.runsSel = 0

	a.handleRunsPanelKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	list := a.activity.List()
	if list[0].ID != h.Activity.ID || list[0].State != activity.StateKilled {
		t.Fatalf("x must kill the selected activity, got %+v", list[0])
	}
}

func TestRunsPanelSubagentFilter(t *testing.T) {
	a := New(Options{})
	a.agentPool = nil
	a.subagents = []components.SubagentChip{{ID: "e1", Label: "x", State: "done"}}
	a.subagentIdx = map[string]int{"e1": 0}
	a.runsOpen = true
	a.runsFocus = true
	a.runsTab = tabSubagents
	a.runsSel = 1

	a.handleRunsPanelKey(tea.KeyMsg{Type: tea.KeyEnter})
	if a.threadFilter != "e1" {
		t.Fatalf("enter must filter to e1, got %q", a.threadFilter)
	}

	a.runsSel = 0
	a.handleRunsPanelKey(tea.KeyMsg{Type: tea.KeyEnter})
	if a.threadFilter != "" {
		t.Fatalf("enter on main must clear filter, got %q", a.threadFilter)
	}
}

func TestRunsPanelFitsSmallFrames(t *testing.T) {
	for _, h := range []int{6, 9, 10, 24, 80} {
		a := New(Options{})
		a.width, a.height = 40, h
		a.activity = activity.NewRegistry()
		a.Update(tea.KeyMsg{Type: tea.KeyF9})
		if !a.runsOpen {
			t.Fatalf("height %d: f9 did not open panel", h)
		}
		got := a.runsPanelHeight()
		if got > h {
			t.Fatalf("height %d: panel height %d exceeds frame", h, got)
		}
		if h >= 12 && got > h/3 {
			t.Fatalf("height %d: panel height %d > h/3", h, got)
		}
	}
}

func TestRunsPanelDoesNotOpenOnTinyFrames(t *testing.T) {
	a := New(Options{})
	a.width, a.height = 40, 5
	a.Update(tea.KeyMsg{Type: tea.KeyF9})
	if a.runsOpen {
		t.Fatalf("panel opened on height %d", a.height)
	}
}
