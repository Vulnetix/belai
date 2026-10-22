package vulnetixcli

import "testing"

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in   string
		want Version
		ok   bool
	}{
		{"vv3.107.2", Version{Major: 3, Minor: 107, Patch: 2, Raw: "vv3.107.2"}, true},
		{"v3.107.2", Version{Major: 3, Minor: 107, Patch: 2, Raw: "v3.107.2"}, true},
		{"3.107.2", Version{Major: 3, Minor: 107, Patch: 2, Raw: "3.107.2"}, true},
		{"3.107.2-beta", Version{Major: 3, Minor: 107, Patch: 2, Pre: "beta", Raw: "3.107.2-beta"}, true},
		{"Vulnetix CLI vv3.107.2", Version{Major: 3, Minor: 107, Patch: 2, Raw: "Vulnetix CLI vv3.107.2"}, true},
		{"1.0.0", Version{Major: 1, Minor: 0, Patch: 0, Raw: "1.0.0"}, true},
		{"not a version", Version{Raw: "not a version"}, false},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := ParseVersion(tc.in)
			if ok != tc.ok {
				t.Fatalf("ParseVersion(%q) ok = %v, want %v", tc.in, ok, tc.ok)
			}
			if got != tc.want {
				t.Fatalf("ParseVersion(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

func TestVersionCompare(t *testing.T) {
	v107 := Version{Major: 1, Minor: 0, Patch: 7}
	v108 := Version{Major: 1, Minor: 0, Patch: 8}
	if v107.Compare(v108) >= 0 {
		t.Fatalf("1.0.7 should be less than 1.0.8")
	}
	if v108.Compare(v107) <= 0 {
		t.Fatalf("1.0.8 should be greater than 1.0.7")
	}
	if v107.Compare(v107) != 0 {
		t.Fatalf("identical versions should compare equal")
	}
	release := Version{Major: 1, Minor: 0, Patch: 0}
	pre := Version{Major: 1, Minor: 0, Patch: 0, Pre: "rc1"}
	if pre.Compare(release) >= 0 {
		t.Fatalf("pre-release should sort before release")
	}
}

// TestParseVersionRoundTrip checks ParseVersion(String(v)) round-trips for parsed versions.
func TestParseVersionRoundTrip(t *testing.T) {
	for _, in := range []string{"v1.0.0", "vv2.3.4", "3.5.6-beta", "v0.0.0-alpha"} {
		v, ok := ParseVersion(in)
		if !ok {
			t.Fatalf("ParseVersion(%q) failed", in)
		}
		got, ok := ParseVersion(v.String())
		if !ok {
			t.Fatalf("ParseVersion(String()) failed for %q", in)
		}
		if !sameVersion(got, v) {
			t.Fatalf("round-trip mismatch for %q: got %+v want %+v", in, got, v)
		}
	}
}

// TestVersionCompareTotalOrder verifies Compare is antisymmetric and transitive on a small grid.
func TestVersionCompareTotalOrder(t *testing.T) {
	versions := []Version{
		{Major: 1, Minor: 0, Patch: 0},
		{Major: 1, Minor: 0, Patch: 0, Pre: "rc1"},
		{Major: 1, Minor: 0, Patch: 1},
		{Major: 1, Minor: 1, Patch: 0},
		{Major: 2, Minor: 0, Patch: 0},
	}
	for i, a := range versions {
		for j, b := range versions {
			ab := a.Compare(b)
			ba := b.Compare(a)
			if i == j && ab != 0 {
				t.Fatalf("self comparison not zero: %+v", a)
			}
			if ab != -ba {
				t.Fatalf("antisymmetry failed for %+v vs %+v: %d vs %d", a, b, ab, ba)
			}
			if ab == 0 && ba == 0 && a != b {
				t.Fatalf("equal compare but values differ: %+v vs %+v", a, b)
			}
		}
	}
}

// TestParseVersionNeverPanics checks ParseVersion on random-looking strings.
func TestParseVersionNeverPanics(t *testing.T) {
	inputs := []string{"", "v", "1.2", "1.2.3.4", "version: 1.2.3", "v1.2.3-rc.1"}
	for _, in := range inputs {
		_, _ = ParseVersion(in)
	}
}

func sameVersion(a, b Version) bool {
	return a.Major == b.Major && a.Minor == b.Minor && a.Patch == b.Patch && a.Pre == b.Pre
}
