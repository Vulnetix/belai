package tui

import (
	"testing"

	"github.com/vulnetix/belai/internal/config"
)

func TestCycleReachesAutoAndLeavesNoStickyChoice(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	a.mode = "agent"
	a.modeAuto = false

	want := []struct {
		mode   string
		auto   bool
		sticky bool
	}{
		{"plan", false, true},
		{"goal", false, true},
		{"code", false, true},
		{"agent", true, false},
		{"agent", false, true},
	}
	for i, w := range want {
		a.cycleMode()
		if a.mode != w.mode || a.modeAuto != w.auto || a.modeSticky != w.sticky || a.modeExplicit != w.sticky {
			t.Fatalf("press %d: mode=%q auto=%v sticky=%v explicit=%v, want %+v", i+1, a.mode, a.modeAuto, a.modeSticky, a.modeExplicit, w)
		}
	}
}

func TestAutoChipAndPersistence(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})
	a.mode = "code"
	a.cycleMode() // code -> auto
	a.refreshFooter()
	if a.footer.Mode != "auto" || a.footer.Agent != "agent" {
		t.Fatalf("chip = %q/%q, want auto/agent", a.footer.Mode, a.footer.Agent)
	}
	// A decision that lands in auto is shown in the profile slot.
	a.mode = "plan"
	a.refreshFooter()
	if a.footer.Mode != "auto" || a.footer.Agent != "plan" {
		t.Fatalf("chip = %q/%q, want auto/plan", a.footer.Mode, a.footer.Agent)
	}
	prefs, err := config.LoadProjectPrefs(workdir)
	if err != nil {
		t.Fatal(err)
	}
	if prefs.Mode != "auto" {
		t.Fatalf("prefs.Mode = %q, want auto", prefs.Mode)
	}
	// From auto, the next press is agent whatever mode the classifier last chose.
	a.cycleMode()
	if a.mode != "agent" || a.modeAuto || !a.modeSticky {
		t.Fatalf("after auto: mode=%q auto=%v sticky=%v", a.mode, a.modeAuto, a.modeSticky)
	}

	b := New(Options{Workdir: workdir})
	b.mode, b.modeAuto = "goal", false
	b.setOperatingMode("auto")
	if !b.modeAuto || b.modeSticky || b.mode != "agent" {
		t.Fatalf("/mode auto: mode=%q auto=%v sticky=%v", b.mode, b.modeAuto, b.modeSticky)
	}
	b.setOperatingMode("plan")
	if b.modeAuto || !b.modeSticky {
		t.Fatalf("/mode plan should clear auto: auto=%v sticky=%v", b.modeAuto, b.modeSticky)
	}
}

func TestRestoredAutoIsNotSticky(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})
	a.mode = "code"
	a.cycleMode() // auto, saved to prefs
	b := New(Options{Workdir: workdir})
	if !b.modeAuto || b.modeSticky || b.modeExplicit || b.mode != "agent" {
		t.Fatalf("restored: mode=%q auto=%v sticky=%v explicit=%v", b.mode, b.modeAuto, b.modeSticky, b.modeExplicit)
	}
}
