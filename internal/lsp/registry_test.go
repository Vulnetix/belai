package lsp

import (
	"sort"
	"testing"
)

func TestLanguagesHaveExtensionOrBasename(t *testing.T) {
	for _, l := range Languages() {
		if len(l.Exts) == 0 && len(l.Basenames) == 0 {
			t.Errorf("%s has no extensions or basenames", l.ID)
		}
	}
}

func TestNoExtensionMapsToTwoLanguages(t *testing.T) {
	seen := map[string]string{}
	for _, l := range Languages() {
		for _, e := range l.Exts {
			if prev, ok := seen[e]; ok {
				t.Errorf("extension %q maps to both %s and %s", e, prev, l.ID)
			}
			seen[e] = l.ID
		}
	}
}

func TestInstallArgvAllowed(t *testing.T) {
	allowed := map[string]bool{
		"go": true, "npm": true, "pip": true, "pip3": true,
		"gem": true, "rustup": true, "dotnet": true, "brew": true, "apt": true,
	}
	for _, l := range Languages() {
		if len(l.Install) == 0 {
			continue
		}
		if !allowed[l.Install[0]] {
			t.Errorf("%s install argv %v has disallowed first element", l.ID, l.Install)
		}
		joined := ""
		for _, a := range l.Install {
			joined += a
		}
		for _, bad := range []string{";", "&", "|", "$", "`", "<", ">", "\"", "'"} {
			for _, a := range l.Install {
				if a == bad {
					t.Errorf("%s install argv contains shell metacharacter %q", l.ID, bad)
				}
			}
		}
	}
}

func TestLanguageIDsStableAndUnique(t *testing.T) {
	ids := LanguageIDs()
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("duplicate language id %q", id)
		}
		seen[id] = true
	}
	sort.Strings(ids)
	second := LanguageIDs()
	sort.Strings(second)
	if !equalSlices(ids, second) {
		t.Fatal("LanguageIDs should be stable across calls")
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestLanguageFor(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"foo.go", "go"},
		{"foo.ts", "ts"},
		{"foo.tsx", "ts"},
		{"Rakefile", "ruby"},
		{"Gemfile", "ruby"},
		{"foo.unknown", ""},
	}
	for _, c := range cases {
		l := LanguageFor(c.path)
		var got string
		if l != nil {
			got = l.ID
		}
		if got != c.want {
			t.Errorf("LanguageFor(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}
