package tui

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/activity"
	"github.com/vulnetix/signet/internal/run"
)

func TestDrawerWidthMath(t *testing.T) {
	a := New(Options{})
	a.width = 100 // contentWidth = 98

	if got := a.drawerWidth(); got != 3 {
		t.Fatalf("closed drawer width = %d, want 3", got)
	}
	if got := a.chatWidth(); got != 95 {
		t.Fatalf("closed chat width = %d, want 95", got)
	}

	a.activityOpen = true
	if got := a.drawerWidth(); got != 86 {
		t.Fatalf("open drawer width = %d, want 86 (clamped to the 12-column chat floor)", got)
	}
	if got := a.chatWidth(); got != 12 {
		t.Fatalf("open chat width = %d, want 12 (the floor)", got)
	}
}

func TestF9TogglesActivityDrawer(t *testing.T) {
	a := New(Options{})

	a.Update(tea.KeyMsg{Type: tea.KeyF9})
	if !a.activityOpen || !a.activityFocus {
		t.Fatal("f9 must open and focus the drawer")
	}
	a.Update(tea.KeyMsg{Type: tea.KeyF9})
	if a.activityFocus {
		t.Fatal("second f9 must return focus to the composer")
	}
	if !a.activityOpen {
		t.Fatal("second f9 must keep the drawer open")
	}
	a.Update(tea.KeyMsg{Type: tea.KeyF9})
	if a.activityOpen || a.activityFocus {
		t.Fatal("third f9 must close the drawer")
	}
}

func TestActivityDrawerFocusRouting(t *testing.T) {
	a := New(Options{})
	a.activityOpen = true
	a.activityFocus = true
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEsc})
	if a.activityFocus {
		t.Fatal("esc must return focus to the composer")
	}
	if !a.activityOpen {
		t.Fatal("esc must keep the drawer open")
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

func TestFlushPendingActivitySendsWaitsWhileBusy(t *testing.T) {
	a := New(Options{})
	a.ctx, a.cancel = context.WithCancel(context.Background())
	a.pendingActivitySends = []activitySend{{label: "a", atts: []run.Attachment{{Kind: "shell", Label: "a", Body: "x"}}}}
	if cmd := a.flushPendingActivitySends(); cmd != nil {
		t.Fatal("busy flush must not send")
	}
	if len(a.pendingActivitySends) != 1 {
		t.Fatal("busy flush must keep the pending send")
	}
}

func TestActivityKillRouting(t *testing.T) {
	a := New(Options{})
	a.activity = activity.NewRegistry()
	h := a.activity.Add(activity.Activity{Kind: activity.KindShell, Label: "!x", State: activity.StateRunning}, func() {})
	a.activityOpen = true
	a.activityFocus = true
	a.activitySel = 0

	a.handleActivityKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	list := a.activity.List()
	if list[0].ID != h.Activity.ID || list[0].State != activity.StateKilled {
		t.Fatalf("x must kill the selected activity, got %+v", list[0])
	}
}
