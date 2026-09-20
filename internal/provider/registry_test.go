package provider

import (
	"sort"
	"testing"
)

func TestRegistryCompleteness(t *testing.T) {
	for name, d := range registry {
		if d.Name == "" {
			t.Errorf("%s: descriptor has no name", name)
		}
		if d.Name != name {
			t.Errorf("%s: descriptor name %q does not match registry key", name, d.Name)
		}
		if !d.Auth.Valid() {
			t.Errorf("%s: auth %q is not valid", name, d.Auth)
		}
		if len(d.Fields) == 0 {
			t.Errorf("%s: descriptor has no fields", name)
		}
		if d.BaseURL == "" && d.BaseURLBuilder == nil {
			t.Errorf("%s: descriptor must have BaseURL or BaseURLBuilder", name)
		}
		for _, f := range d.Fields {
			if f.Name == "" {
				t.Errorf("%s: field has no name", name)
			}
			if len(f.EnvVars) == 0 {
				t.Errorf("%s.%s: field has no env vars", name, f.Name)
			}
		}
	}
}

func TestRegistryNamesStableAndRoundTrip(t *testing.T) {
	names := Names()
	if len(names) != len(registry) {
		t.Fatalf("Names() returned %d entries, want %d", len(names), len(registry))
	}
	if !sort.StringsAreSorted(names) {
		t.Fatalf("Names() is not sorted: %v", names)
	}
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			t.Fatalf("duplicate name %q", n)
		}
		seen[n] = true
		if !Builtin(n) {
			t.Fatalf("Builtin(%q) false for registry name", n)
		}
		if _, ok := Lookup(n); !ok {
			t.Fatalf("Lookup(%q) failed", n)
		}
	}
}

func TestBuiltinMatchesLookup(t *testing.T) {
	for _, name := range []string{"openai", "anthropic", "ollama", "unknown-provider"} {
		got := Builtin(name)
		_, ok := Lookup(name)
		if got != ok {
			t.Fatalf("Builtin(%q) = %v, Lookup ok = %v", name, got, ok)
		}
	}
}
