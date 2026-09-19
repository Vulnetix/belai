package bgagent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vulnetix/signet/internal/agentprofile"
	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/run"
)

// TestSetPostureUpdatesLiveInstance pins the live half of the background-agent
// guardrails switch: SetPosture walks running instances and updates their own
// Live through choosePosture, so a toggle lands on the next gate check inside
// an agent already mid-turn rather than only on its next session.
func TestSetPostureUpdatesLiveInstance(t *testing.T) {
	workdir := t.TempDir()
	// The project layer only tightens. Relax tool_call_mismatch so the merge
	// is observable: the manager's AllIgnore reaches the instance only where
	// the project permits it.
	prefs := filepath.Join(config.ProjectSignetDir(workdir), "preferences.yaml")
	if err := os.MkdirAll(filepath.Dir(prefs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prefs, []byte("postures:\n  tool_call_mismatch: ignore\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := NewManager(workdir, run.Config{Provider: "openai", Model: "test"}, nil, config.Settings{}, posture.Defaults())
	inst := &AgentInstance{
		Profile: agentprofile.AgentProfile{
			Name: "live", Description: "d", SystemPrompt: "s", Mode: agentprofile.ModeSingle,
		},
		Events:  make(chan Event, 1),
		workdir: workdir,
		// Seed exactly as StartIn does: the manager posture merged with the
		// project posture.
		live: posture.NewLive(choosePosture(posture.Defaults(), workdir), false),
	}
	m.mu.Lock()
	m.agents["live"] = inst
	m.mu.Unlock()

	// With the manager at Defaults the merge yields enforce (stricter of
	// enforce and the project's ignore).
	if got := inst.live.Level(posture.ToolCallMismatch); got != posture.Enforce {
		t.Fatalf("precondition: level = %s, want enforce", got)
	}

	m.SetPosture(posture.AllIgnore())

	if got := inst.live.Level(posture.ToolCallMismatch); got != posture.Ignore {
		t.Fatalf("live instance level after SetPosture = %s, want ignore", got)
	}
	// The manager also keeps the new policy for future sessions.
	if got := m.Posture().Level(posture.ToolCallMismatch); got != posture.Ignore {
		t.Fatalf("manager posture after SetPosture = %s, want ignore", got)
	}
}
