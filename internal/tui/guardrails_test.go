package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/posture"
)

// TestToggleGuardrailsMovesLive pins the live-posture half of the f3 toggle:
// the shared Live flips immediately, and the plan-mode surface for the next
// session is built from the effective switch rather than the persisted setting.
func TestToggleGuardrailsMovesLive(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	a := New(Options{})
	if !a.guardrailsEnabled() {
		t.Fatal("precondition: guardrails should start on")
	}
	if got := a.live.Level(posture.ToolResultUnsafe); got != posture.Enforce {
		t.Fatalf("precondition: live level = %s, want enforce", got)
	}

	_, _ = a.Update(tea.KeyMsg{Type: tea.KeyF3})
	if a.guardrailsEnabled() {
		t.Fatal("f3 should turn guardrails off")
	}
	if got := a.live.Level(posture.ToolResultUnsafe); got != posture.Ignore {
		t.Fatalf("live level after f3 = %s, want ignore", got)
	}
	if p := a.sessionBuildParams(); p.guardrails {
		t.Fatal("sessionBuildParams().guardrails should be false after f3")
	}

	_, _ = a.Update(tea.KeyMsg{Type: tea.KeyF3})
	if !a.guardrailsEnabled() {
		t.Fatal("second f3 should turn guardrails back on")
	}
	if got := a.live.Level(posture.ToolResultUnsafe); got != posture.Enforce {
		t.Fatalf("live level after second f3 = %s, want enforce", got)
	}
	if p := a.sessionBuildParams(); !p.guardrails {
		t.Fatal("sessionBuildParams().guardrails should be true after second f3")
	}
}

// TestToggleAskMovesLive pins the ask half: f4 flips the shared holder's ask
// gate without waiting for the next session build.
func TestToggleAskMovesLive(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	a := New(Options{})
	if !a.askEnabled() {
		t.Fatal("precondition: ask should start on")
	}
	if a.live.AskDisabled() {
		t.Fatal("precondition: live ask should be enabled")
	}

	_, _ = a.Update(tea.KeyMsg{Type: tea.KeyF4})
	if a.askEnabled() {
		t.Fatal("f4 should turn ask off")
	}
	if !a.live.AskDisabled() {
		t.Fatal("live ask should be disabled after f4")
	}

	_, _ = a.Update(tea.KeyMsg{Type: tea.KeyF4})
	if !a.askEnabled() {
		t.Fatal("second f4 should turn ask back on")
	}
	if a.live.AskDisabled() {
		t.Fatal("live ask should be enabled after second f4")
	}
}

// TestToggleAskOffResolvesPendingAsk pins the pending-prompt contract: turning
// ask off while the approval view is on screen answers allow-once and dismisses
// it, so the blocked agent loop is not left waiting on a gate that is now off.
func TestToggleAskOffResolvesPendingAsk(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	a := New(Options{})
	reply := make(chan agent.PermissionAskReply, 1)
	a.permAskState = newPermissionAskState(sampleAskRequest(), reply)
	a.push(viewPermissionAsk)

	_, _ = a.Update(tea.KeyMsg{Type: tea.KeyF4})

	select {
	case r := <-reply:
		if !r.Allow {
			t.Fatal("pending ask should resolve as allow-once")
		}
	case <-time.After(time.Second):
		t.Fatal("pending ask was not answered")
	}
	if a.permAskState.reply != nil {
		t.Fatal("pending ask state was not cleared")
	}
	if a.view == viewPermissionAsk {
		t.Fatal("permission-ask view was not dismissed")
	}
}

// TestSetYoloMovesLiveAndResolvesPendingAsk pins /yolo on: both gates drop to
// off in the shared holder, and a pending ask is auto-allowed like f4.
func TestSetYoloMovesLiveAndResolvesPendingAsk(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	a := New(Options{})
	reply := make(chan agent.PermissionAskReply, 1)
	a.permAskState = newPermissionAskState(sampleAskRequest(), reply)
	a.push(viewPermissionAsk)

	a.setYolo(true)

	if a.guardrailsEnabled() || a.askEnabled() {
		t.Fatal("yolo on should turn both gates off")
	}
	if got := a.live.Level(posture.ToolResultUnsafe); got != posture.Ignore {
		t.Fatalf("live level after yolo = %s, want ignore", got)
	}
	if !a.live.AskDisabled() {
		t.Fatal("live ask should be disabled after yolo")
	}
	select {
	case r := <-reply:
		if !r.Allow {
			t.Fatal("pending ask should resolve as allow-once")
		}
	case <-time.After(time.Second):
		t.Fatal("pending ask was not answered")
	}
	if a.permAskState.reply != nil {
		t.Fatal("pending ask state was not cleared")
	}
	if a.view == viewPermissionAsk {
		t.Fatal("permission-ask view was not dismissed")
	}
}
