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
