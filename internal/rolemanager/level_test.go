package rolemanager

import "testing"

// TestParseLevel pins the stored-name mapping and its fail-closed default.
func TestParseLevel(t *testing.T) {
	cases := map[string]Level{
		"decisions":  LevelDecisions,
		" DECISIONS": LevelDecisions,
		"Security\n": LevelSecurity,
		"all":        LevelAll,
		"":           LevelHidden,
		"hidden":     LevelHidden,
		"bogus":      LevelHidden,
	}
	for in, want := range cases {
		if got := ParseLevel(in); got != want {
			t.Errorf("ParseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}
