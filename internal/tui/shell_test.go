package tui

import (
	"strings"
	"testing"
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

func TestShellEchoProducesSystemMessage(t *testing.T) {
	a := New(Options{})
	a.mode = "agent"
	cmd := a.handleShell("!echo hello-signet")
	if cmd == nil {
		t.Fatalf("expected a command")
	}
	msg := cmd()
	if _, ok := msg.(shellDoneMsg); !ok {
		t.Fatalf("expected shellDoneMsg, got %T", msg)
	}
}
