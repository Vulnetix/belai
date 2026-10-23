package config

import "testing"

func TestA2ASettingsDefaults(t *testing.T) {
	var s Settings
	if !s.A2AEnabled() || s.A2AFilter() != A2AFilterRepo || !s.A2AVisible() {
		t.Fatal("A2A must default on, repo filter, shown")
	}
	f := false
	s.UI = &UISettings{ShowA2A: &f}
	s.A2A = &A2ASettings{Enabled: &f, Filter: "bogus"}
	if s.A2AEnabled() || s.A2AVisible() || s.A2AFilter() != A2AFilterRepo {
		t.Fatal("A2A settings not read")
	}
}

func TestA2AProjectLayerOnlyNarrows(t *testing.T) {
	on, off := true, false
	global := Settings{A2A: &A2ASettings{Enabled: &on, Filter: A2AFilterAll}}
	got := global.Override(Settings{A2A: &A2ASettings{Filter: A2AFilterRepo}})
	if got.A2AFilter() != A2AFilterRepo {
		t.Fatalf("project could not narrow: %s", got.A2AFilter())
	}
	got = Settings{A2A: &A2ASettings{Filter: A2AFilterOff}}.Override(Settings{A2A: &A2ASettings{Filter: A2AFilterAll}})
	if got.A2AFilter() != A2AFilterOff {
		t.Fatal("project widened the filter")
	}
	got = Settings{A2A: &A2ASettings{Enabled: &off}}.Override(Settings{A2A: &A2ASettings{Enabled: &on}})
	if got.A2AEnabled() {
		t.Fatal("project turned A2A on")
	}
	got = global.Override(Settings{A2A: &A2ASettings{Enabled: &off}})
	if got.A2AEnabled() {
		t.Fatal("project could not turn A2A off")
	}
}
