package bgproc

import "testing"

// TestSetSessionID pins that the recovery subagent's session id starts empty
// and follows the latest SetSessionID.
func TestSetSessionID(t *testing.T) {
	m := testManager(t)
	if got := m.sessionID(); got != "" {
		t.Fatalf("initial session = %q, want empty", got)
	}
	m.SetSessionID("sess-1")
	if got := m.sessionID(); got != "sess-1" {
		t.Fatalf("session = %q", got)
	}
}
