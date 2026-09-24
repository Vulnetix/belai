package bgagent

import (
	"testing"

	"github.com/vulnetix/signet/internal/agentpool"
	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/run"
)

// TestSetCredentialSourceAndPool pins the two resolver attachments: both write
// through the mutex and the credential source is readable again for re-resolving
// a profile's provider override.
func TestSetCredentialSourceAndPool(t *testing.T) {
	m := NewManager(t.TempDir(), run.Config{}, nil, config.Settings{}, posture.Defaults())

	src := run.EnvSource(func(string) string { return "" })
	m.SetCredentialSource(src)
	if m.src == nil {
		t.Fatal("SetCredentialSource did not install the resolver")
	}

	p := agentpool.New(2)
	m.SetPool(p)
	m.mu.RLock()
	got := m.pool
	m.mu.RUnlock()
	if got != p {
		t.Fatal("SetPool did not attach the pool")
	}
	m.SetPool(nil)
	m.mu.RLock()
	got = m.pool
	m.mu.RUnlock()
	if got != nil {
		t.Fatal("SetPool(nil) did not detach the pool")
	}
}

// TestStricterLevelMatrix pins the three-valued merge: Enforce > Warn > Ignore.
func TestStricterLevelMatrix(t *testing.T) {
	cases := []struct {
		a, b posture.Level
		want posture.Level
	}{
		{posture.Enforce, posture.Enforce, posture.Enforce},
		{posture.Enforce, posture.Warn, posture.Enforce},
		{posture.Enforce, posture.Ignore, posture.Enforce},
		{posture.Warn, posture.Enforce, posture.Enforce},
		{posture.Warn, posture.Warn, posture.Warn},
		{posture.Warn, posture.Ignore, posture.Warn},
		{posture.Ignore, posture.Enforce, posture.Enforce},
		{posture.Ignore, posture.Warn, posture.Warn},
		{posture.Ignore, posture.Ignore, posture.Ignore},
	}
	for _, c := range cases {
		if got := stricterLevel(c.a, c.b); got != c.want {
			t.Errorf("stricterLevel(%s, %s) = %s, want %s", c.a, c.b, got, c.want)
		}
	}
}

// TestChoosePostureNoProjectPins the error path: with no project posture on
// disk the manager's posture is returned unchanged (same values per gate).
func TestChoosePostureNoProject(t *testing.T) {
	pol := posture.Defaults()
	got := choosePosture(pol, t.TempDir())
	for _, g := range posture.AllGates {
		if got.Level(g) != pol.Level(g) {
			t.Fatalf("choosePosture with no project posture = %v, want the manager posture", got)
		}
	}
}

// TestReleaseLeaseNilPins the no-op guard: a nil lease must not panic or emit.
func TestReleaseLeaseNil(t *testing.T) {
	m := NewManager(t.TempDir(), run.Config{}, nil, config.Settings{}, posture.Defaults())
	inst := newBareInstance("x")
	m.releaseLease(inst, nil)
	select {
	case e := <-inst.Events:
		t.Fatalf("nil lease must not emit an event, got %+v", e)
	default:
	}
}

// TestResumeAndPauseWrongState pins the two error branches: resuming a
// non-paused agent and pausing a non-running agent both fail cleanly.
func TestResumeAndPauseWrongState(t *testing.T) {
	m := NewManager(t.TempDir(), run.Config{}, nil, config.Settings{}, posture.Defaults())
	inst := newBareInstance("x")
	m.mu.Lock()
	m.agents["x"] = inst
	m.mu.Unlock()

	// Not present yet -> not found.
	if err := m.Resume("nope"); err == nil {
		t.Fatal("Resume on an unknown agent should fail")
	}
	if err := m.Pause("nope"); err == nil {
		t.Fatal("Pause on an unknown agent should fail")
	}

	// Idle (not running) -> Pause fails; idle (not paused) -> Resume fails.
	if err := m.Pause("x"); err == nil {
		t.Fatal("Pause on an idle agent should fail")
	}
	if err := m.Resume("x"); err == nil {
		t.Fatal("Resume on an idle agent should fail")
	}
}
