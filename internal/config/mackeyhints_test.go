package config

import "testing"

// An unset ui.mac_key_hints follows the platform, and an explicit value wins
// on every platform, so a Linux VM on Mac hardware can turn the hints on.
func TestMacKeyHintsFollowThePlatformUnlessSet(t *testing.T) {
	tr, fa := true, false
	cases := []struct {
		name string
		s    Settings
		goos string
		want bool
	}{
		{"unset darwin", Settings{}, "darwin", true},
		{"unset linux", Settings{}, "linux", false},
		{"unset windows", Settings{}, "windows", false},
		{"empty ui block darwin", Settings{UI: &UISettings{}}, "darwin", true},
		{"on in a linux vm", Settings{UI: &UISettings{MacKeyHints: &tr}}, "linux", true},
		{"off on a mac", Settings{UI: &UISettings{MacKeyHints: &fa}}, "darwin", false},
	}
	for _, c := range cases {
		if got := c.s.MacKeyHintsFor(c.goos); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestMacKeyHintsOverrideMerges(t *testing.T) {
	tr := true
	var u UISettings
	u.merge(&UISettings{MacKeyHints: &tr})
	if u.MacKeyHints == nil || !*u.MacKeyHints {
		t.Fatal("merge dropped mac_key_hints")
	}
}
