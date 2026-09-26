package bgagent

import (
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/run"
)

// TestSetSessionID pins that the owning session id starts empty and follows
// the latest SetSessionID (new, resume, fork all call it again).
func TestSetSessionID(t *testing.T) {
	m := NewManager(t.TempDir(), run.Config{}, nil, config.Settings{}, posture.Defaults())
	if got := m.sessionID(); got != "" {
		t.Fatalf("initial session = %q, want empty", got)
	}
	m.SetSessionID("a")
	m.SetSessionID("b")
	if got := m.sessionID(); got != "b" {
		t.Fatalf("session = %q, want b", got)
	}
}
