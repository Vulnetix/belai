package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/tui/components"
)

// The hint line belongs to the working composer: an idle composer keeps the
// row for the transcript.
func TestWorkingHintOnlyWhileWorking(t *testing.T) {
	a := New(Options{Provider: "openai", Model: "gpt-5", Workdir: t.TempDir()})
	a.width, a.height = 100, 40
	if got := a.workingHint(); got != "" {
		t.Fatalf("idle composer showed a hint: %q", got)
	}
	if h := a.hintHeight(); h != 0 {
		t.Fatalf("idle hint height = %d, want 0", h)
	}
	a.setPhaseWorking()
	if got := a.workingHint(); got == "" {
		t.Fatal("working composer showed no hint")
	}
	if h := a.hintHeight(); h != 1 {
		t.Fatalf("working hint height = %d, want 1", h)
	}
	a.endPhase()
	if got := a.workingHint(); got != "" {
		t.Fatalf("finished turn kept a hint: %q", got)
	}
}

// The hint has to reach the frame above the composer, not just the helper.
func TestWorkingHintRendersAboveTheComposer(t *testing.T) {
	a := New(Options{Provider: "openai", Model: "gpt-5", Workdir: t.TempDir()})
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	if strings.Contains(a.chatView(), "hint · ") {
		t.Fatal("idle frame carried a hint line")
	}
	a.setPhaseWorking()
	frame := a.chatView()
	hintIdx := strings.Index(frame, "hint · ")
	if hintIdx < 0 {
		t.Fatal("working frame carried no hint line")
	}
	if askIdx := strings.Index(frame, "ask"); askIdx >= 0 && askIdx < hintIdx {
		t.Fatal("hint line must render above the composer, not below it")
	}
}

// A long turn rotates: the hint after one rotation interval is the next tip in
// the table, not the one already on screen.
func TestWorkingHintRotatesWithElapsedTime(t *testing.T) {
	a := New(Options{Provider: "openai", Model: "gpt-5", Workdir: t.TempDir()})
	a.width, a.height = 100, 40
	a.setPhaseWorking()
	first := a.workingHint()
	a.phaseStartedAt = time.Now().Add(-hintRotate - time.Second)
	second := a.workingHint()
	if first == second {
		t.Fatalf("hint did not rotate after %s: %q", hintRotate, first)
	}
	if !strings.Contains(second, components.CycleTip(a.sessionID, 1)) {
		t.Fatalf("hint %q is not the second tip in the rotation", second)
	}
}

// A first run pins the signup hint everywhere a hint appears: the user cannot
// use any binding until the default provider has an account behind it.
func TestFirstRunPinsTheSignupHint(t *testing.T) {
	a := New(Options{Provider: "openai", Model: "gpt-5", Workdir: t.TempDir()})
	a.width, a.height = 100, 40
	a.firstRun = true
	if got := a.pickBannerTip(); got != components.FirstRunTip {
		t.Fatalf("banner tip = %q, want the first-run tip", got)
	}
	a.setPhaseWorking()
	if !strings.Contains(a.workingHint(), "https://openrouter.ai/") {
		t.Fatalf("working hint on a first run = %q", a.workingHint())
	}
}
