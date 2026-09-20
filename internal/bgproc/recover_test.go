package bgproc

import (
	"net/http"
	"testing"
	"time"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/tools"
)

// TestUnsafeTailDroppedStillRecovers verifies that when the tail is dropped by
// the posture gate the recovery subagent still runs from the harness-owned
// facts (command + exit code). Without a real classifier the subagent does
// nothing, but the attempt counter is still consumed.
func TestUnsafeTailDroppedStillRecovers(t *testing.T) {
	settings := config.Settings{Resilience: &config.ResilienceSettings{MaxProcessRecoveries: 1}}
	dir := t.TempDir()
	home := t.TempDir()
	t.Setenv("SIGNET_HOME", home)
	m := NewManager(dir, run.Config{}, &http.Client{}, settings, posture.Defaults(), tools.Capabilities{})

	if _, err := m.Start("false", "false"); err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	p, ok := m.Lookup("p0")
	if !ok || p.State != StateRecovering {
		t.Fatalf("expected recovering, got %+v", p)
	}
	sawRecover := false
	drainEvents(m, func(e Event) bool {
		if e.Kind == "recover" {
			sawRecover = true
		}
		return sawRecover
	})
	if !sawRecover {
		t.Fatal("expected recover event")
	}
}
