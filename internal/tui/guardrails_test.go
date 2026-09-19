package tui

import (
	"testing"

	"github.com/vulnetix/signet/internal/bgagent"
	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/run"
)

// TestGuardrailsAskOverrides pins the operator-control state machine behind
// the footer chips and the /yolo command: defaults on, /yolo on forces both
// off, toggles flip each independently, and /yolo off restores settings.
func TestGuardrailsAskOverrides(t *testing.T) {
	a := &App{settings: config.Settings{}}

	if !a.guardrailsEnabled() || !a.askEnabled() {
		t.Fatal("guardrails and ask must default to on")
	}

	a.setYolo(true)
	if a.guardrailsEnabled() || a.askEnabled() {
		t.Fatal("/yolo on must force both off")
	}

	a.toggleGuardrails()
	if !a.guardrailsEnabled() {
		t.Fatal("f3 must flip guardrails back on")
	}
	if a.askEnabled() {
		t.Fatal("toggling guardrails must not change ask")
	}

	a.toggleAsk()
	if !a.askEnabled() {
		t.Fatal("f4 must flip ask back on")
	}

	a.setYolo(false)
	if !a.guardrailsEnabled() || !a.askEnabled() {
		t.Fatal("/yolo off must restore the settings-file values (both on)")
	}
}

// TestGuardrailsAskSettingsFold pins the settings accessors: nil means on,
// false means off, and the project layer may only tighten (turn back on).
func TestGuardrailsAskSettingsFold(t *testing.T) {
	var zero config.Settings
	if !zero.GuardrailsEnabled() || !zero.AskPermissionEnabled() {
		t.Fatal("zero settings must default guardrails/ask on")
	}

	off := false
	zero.Guardrails = &off
	zero.AskPermission = &off
	if zero.GuardrailsEnabled() || zero.AskPermissionEnabled() {
		t.Fatal("explicit false must disable")
	}

	on := true
	merged := (config.Settings{}).Override(config.Settings{Guardrails: &on, AskPermission: &on})
	if !merged.GuardrailsEnabled() || !merged.AskPermissionEnabled() {
		t.Fatal("project true must tighten")
	}

	loose := config.Settings{Guardrails: &off, AskPermission: &off}
	still := (config.Settings{Guardrails: &on, AskPermission: &on}).Override(loose)
	if !still.GuardrailsEnabled() || !still.AskPermissionEnabled() {
		t.Fatal("project false must not loosen an already-on global")
	}
}

// The guardrails switch is not a footer decoration: with it off, every gate
// reads Ignore, and with it on the configured posture is unchanged.
func TestEffectivePostureFollowsTheGuardrailsSwitch(t *testing.T) {
	a := &App{settings: config.Settings{}, posture: posture.Defaults()}

	for _, g := range posture.AllGates {
		if got, want := a.effectivePosture().Level(g), posture.DefaultLevel(g); got != want {
			t.Fatalf("guardrails on: gate %q = %q, want %q", g, got, want)
		}
	}

	a.toggleGuardrails()
	for _, g := range posture.AllGates {
		if got := a.effectivePosture().Level(g); got != posture.Ignore {
			t.Errorf("guardrails off: gate %q = %q, want ignore", g, got)
		}
	}

	a.toggleGuardrails()
	if a.effectivePosture().Level(posture.ToolResultUnsafe) != posture.Enforce {
		t.Fatal("turning guardrails back on must restore the configured posture")
	}
}

// /yolo off turns both gates off in one step, and the posture has to follow.
func TestYoloTurnsEveryGateOff(t *testing.T) {
	a := &App{settings: config.Settings{}, posture: posture.Defaults()}
	a.setYolo(true)
	for _, g := range posture.AllGates {
		if got := a.effectivePosture().Level(g); got != posture.Ignore {
			t.Errorf("yolo: gate %q = %q, want ignore", g, got)
		}
	}
	a.setYolo(false)
	if a.effectivePosture().Level(posture.ToolResultUnsafe) != posture.Enforce {
		t.Fatal("/yolo off must restore the configured posture")
	}
}

// A settings file that disables guardrails is honoured with no toggle at all.
func TestSettingsGuardrailsOffReachesThePosture(t *testing.T) {
	off := false
	a := &App{settings: config.Settings{Guardrails: &off}, posture: posture.Defaults()}
	if a.effectivePosture().Level(posture.ToolResultUnsafe) != posture.Ignore {
		t.Fatal("guardrails:false in settings must disable the gates")
	}
}

// The session snapshot carries the *effective* policy, so the async session
// build cannot re-derive it and get a different answer.
func TestSessionSnapshotCarriesTheEffectivePosture(t *testing.T) {
	a := &App{settings: config.Settings{}, posture: posture.Defaults(), workdir: t.TempDir(), live: posture.NewLive(posture.Defaults(), false)}
	a.toggleGuardrails()

	p := a.sessionBuildParams()
	for _, g := range posture.AllGates {
		if got := p.live.Level(g); got != posture.Ignore {
			t.Errorf("session snapshot: gate %q = %q, want ignore", g, got)
		}
	}
}

// Toggling guardrails hands the new policy to a running background-agent
// manager. Without this the manager keeps the policy it was constructed with
// and goes on enforcing gates the footer says are off.
func TestToggleGuardrailsReachesBackgroundAgents(t *testing.T) {
	a := &App{settings: config.Settings{}, posture: posture.Defaults(), workdir: t.TempDir()}
	a.bgManager = bgagent.NewManager(a.workdir, run.Config{}, nil, a.settings, a.effectivePosture())

	a.toggleGuardrails()
	if got := a.bgManager.Posture().Level(posture.ToolResultUnsafe); got != posture.Ignore {
		t.Fatalf("background manager posture = %q after guardrails off, want ignore", got)
	}

	a.toggleGuardrails()
	if got := a.bgManager.Posture().Level(posture.ToolResultUnsafe); got != posture.Enforce {
		t.Fatalf("background manager posture = %q after guardrails on, want enforce", got)
	}
}

// A nil manager must not panic the toggle: the TUI runs without one when no
// provider is configured.
func TestSyncPostureWithoutABackgroundManager(t *testing.T) {
	a := &App{settings: config.Settings{}, posture: posture.Defaults()}
	a.syncPosture() // must not panic
	a.setYolo(true)
}
