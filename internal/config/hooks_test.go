package config

import "testing"

// Hooks default on. The user's global settings may turn them off; a
// repo-visible project file may turn them off but never on, so a cloned
// repository cannot re-enable commands the user switched off.
func TestResolveHooksDirection(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	resolve := func(global, project *bool) bool {
		t.Helper()
		workdir := t.TempDir()
		if err := SaveGlobal(Settings{Hooks: &HooksSettings{Enabled: global}}); err != nil {
			t.Fatal(err)
		}
		if err := SaveProject(workdir, Settings{Hooks: &HooksSettings{Enabled: project}}); err != nil {
			t.Fatal(err)
		}
		eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
		if err != nil {
			t.Fatal(err)
		}
		return eff.Settings.Hooks.HooksEnabled()
	}
	on, off := boolPtr(true), boolPtr(false)
	if !resolve(nil, nil) {
		t.Error("default must be on")
	}
	if resolve(off, nil) {
		t.Error("the global layer must be able to turn hooks off")
	}
	if resolve(nil, off) {
		t.Error("a project file may turn hooks off")
	}
	if resolve(off, on) {
		t.Error("a project file must not turn hooks on")
	}
	if merged := (Settings{Hooks: &HooksSettings{Enabled: off}}).Override(Settings{Hooks: &HooksSettings{Enabled: on}}); merged.Hooks.HooksEnabled() {
		t.Error("Override must not let a project file turn hooks on")
	}
	if merged := (Settings{}).Override(Settings{Hooks: &HooksSettings{Enabled: off}}); merged.Hooks.HooksEnabled() {
		t.Error("Override must let a project file turn hooks off")
	}
}
