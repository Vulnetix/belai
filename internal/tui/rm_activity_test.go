package tui

import (
	"testing"

	"github.com/vulnetix/signet/internal/rolemanager"
)

// TestRMActivityObserverFeedsPanel pins the transport wiring: New registers
// the observer, a role-manager decision lands on the buffered channel, and
// addRMActivity turns it into a render-only rolemanager message.
func TestRMActivityObserverFeedsPanel(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	defer a.rmCancel()

	// ParseSessionName records a session_name decision through the global
	// observer registered by New.
	if _, err := rolemanager.ParseSessionName("my session"); err != nil {
		t.Fatalf("ParseSessionName: %v", err)
	}

	cmd := a.nextRMActivity()
	if cmd == nil {
		t.Fatal("nextRMActivity returned nil")
	}
	msg, ok := cmd().(rmActivityMsg)
	if !ok {
		t.Fatalf("nextRMActivity delivered %T, want rmActivityMsg", msg)
	}
	a.addRMActivity(rolemanager.Activity(msg))

	found := false
	for _, m := range a.messages {
		if m.Role == "rolemanager" && m.RM.Summary != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no rolemanager message appended: %+v", a.messages)
	}
}
