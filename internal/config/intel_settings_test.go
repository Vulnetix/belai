package config

import (
	"strings"
	"testing"
)

// R22: ui.intel turns the intel slot and pane off; it is a UI key, so any layer
// may set it, and it defaults to on.
func TestBudgetRule22_UIIntelDefaultsOnAndAnyLayerMayTurnItOff(t *testing.T) {
	if !(Settings{}).IntelEnabled() || !(Settings{UI: &UISettings{}}).IntelEnabled() {
		t.Fatal("ui.intel is not on by default")
	}
	if (Settings{UI: &UISettings{Intel: boolPtr(false)}}).IntelEnabled() {
		t.Fatal("ui.intel false is still enabled")
	}
	var u UISettings
	u.merge(&UISettings{Intel: boolPtr(false)})
	if u.Intel == nil || *u.Intel {
		t.Fatalf("merge lost ui.intel: %+v", u.Intel)
	}

	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()
	if err := SaveProject(workdir, Settings{UI: &UISettings{Intel: boolPtr(false)}}); err != nil {
		t.Fatal(err)
	}
	eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if eff.Settings.IntelEnabled() {
		t.Fatal("a project layer could not turn ui.intel off")
	}
}

// R22: intel.plan_limits is global: a project layer's value is dropped with a
// note, the global value wins, and it defaults to on.
func TestBudgetRule22_PlanLimitsSettingIsGlobalOnly(t *testing.T) {
	if !(Settings{}).PlanLimitsEnabled() || !(Settings{Intel: &IntelSettings{}}).PlanLimitsEnabled() {
		t.Fatal("intel.plan_limits is not on by default")
	}

	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()
	if err := SaveGlobal(Settings{Intel: &IntelSettings{PlanLimits: boolPtr(true)}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveProject(workdir, Settings{Intel: &IntelSettings{PlanLimits: boolPtr(false)}}); err != nil {
		t.Fatal(err)
	}
	eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if !eff.Settings.PlanLimitsEnabled() {
		t.Fatal("a project layer turned plan-limit parsing off")
	}
	if eff.Origin["intel"] != SourceGlobal {
		t.Fatalf("origin = %q, want global", eff.Origin["intel"])
	}
	found := false
	for _, n := range eff.Notes {
		found = found || strings.Contains(n, "project intel ignored")
	}
	if !found {
		t.Fatalf("notes = %v, want the ignore note", eff.Notes)
	}

	// The global layer may turn it off.
	if err := SaveGlobal(Settings{Intel: &IntelSettings{PlanLimits: boolPtr(false)}}); err != nil {
		t.Fatal(err)
	}
	eff, err = Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if eff.Settings.PlanLimitsEnabled() {
		t.Fatal("the global layer could not turn plan-limit parsing off")
	}
}
