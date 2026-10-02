package config

import "testing"

func bp(v bool) *bool { return &v }

// Every library kind has its own sync switch: default on, off when set off, and
// never on while sync itself is off.
func TestSyncItemEnabled(t *testing.T) {
	kinds := map[string]func(*SyncSettings, *bool){
		"skill":   func(s *SyncSettings, v *bool) { s.Skills = v },
		"prompt":  func(s *SyncSettings, v *bool) { s.Prompts = v },
		"process": func(s *SyncSettings, v *bool) { s.Processes = v },
	}
	for kind, set := range kinds {
		if !(Settings{}).SyncItemEnabled(kind) {
			t.Errorf("%s: off by default", kind)
		}
		on, off := &SyncSettings{}, &SyncSettings{}
		set(on, bp(true))
		set(off, bp(false))
		if !(Settings{Sync: on}).SyncItemEnabled(kind) || (Settings{Sync: off}).SyncItemEnabled(kind) {
			t.Errorf("%s: the switch does not decide", kind)
		}
		// Another kind's switch does not touch it.
		for other, setOther := range kinds {
			if other == kind {
				continue
			}
			o := &SyncSettings{}
			setOther(o, bp(false))
			if !(Settings{Sync: o}).SyncItemEnabled(kind) {
				t.Errorf("%s was turned off by the %s switch", kind, other)
			}
		}
		// sync.enabled false closes every kind.
		all := &SyncSettings{Enabled: bp(false)}
		set(all, bp(true))
		if (Settings{Sync: all}).SyncItemEnabled(kind) {
			t.Errorf("%s is on while sync is off", kind)
		}
	}
	if (Settings{}).SyncItemEnabled("agent") || (Settings{}).SyncItemEnabled("") || (Settings{}).SyncItemEnabled("skills") {
		t.Error("an unknown kind is on")
	}
}

// A project settings file may turn a kind off and never on.
func TestProjectLayerCanOnlySwitchLibrarySyncOff(t *testing.T) {
	user := Settings{Sync: &SyncSettings{Skills: bp(false), Prompts: bp(true)}}
	proj := Settings{Sync: &SyncSettings{Skills: bp(true), Prompts: bp(false), Processes: bp(false)}}
	got := user.Override(proj)
	if got.SyncItemEnabled("skill") {
		t.Error("a project layer turned sync.skills back on")
	}
	if got.SyncItemEnabled("prompt") || got.SyncItemEnabled("process") {
		t.Error("a project layer could not turn a switch off")
	}
	// An absent project key changes nothing.
	same := Settings{Sync: &SyncSettings{Skills: bp(false)}}.Override(Settings{Sync: &SyncSettings{}})
	if same.SyncItemEnabled("skill") {
		t.Error("an empty project block turned a switch on")
	}
}
