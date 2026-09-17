package tui

import (
	"testing"

	"github.com/vulnetix/signet/internal/config"
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
		t.Fatal("ctrl+alt+g must flip guardrails back on")
	}
	if a.askEnabled() {
		t.Fatal("toggling guardrails must not change ask")
	}

	a.toggleAsk()
	if !a.askEnabled() {
		t.Fatal("ctrl+alt+a must flip ask back on")
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
