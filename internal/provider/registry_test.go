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

func TestLocalProvidersExposeOneOptionalSecretField(t *testing.T) {
	for _, name := range []string{"ollama", "llama-server"} {
		d, ok := Lookup(name)
		if !ok {
			t.Fatalf("Lookup(%q) failed", name)
		}
		var secret int
		for _, f := range d.Fields {
			if f.Secret {
				secret++
				if f.Name != "api_key" {
					t.Fatalf("%s: secret field %q is not api_key", name, f.Name)
				}
				if !f.Optional {
					t.Fatalf("%s: api_key field must be optional", name)
				}
			}
		}
		if secret != 1 {
			t.Fatalf("%s: %d secret fields, want exactly one", name, secret)
		}
	}
}

func TestTemplateMapsEveryKind(t *testing.T) {
	cases := map[string]struct {
		ok    bool
		local bool
		list  string
		surf  string
		auth  Auth
	}{
		"ollama":            {ok: true, local: true, list: "/models", surf: "openai-chat", auth: AuthBearer},
		"llama-server":      {ok: true, local: true, list: "/models", surf: "openai-chat", auth: AuthBearer},
		"":                  {ok: true, local: false, list: "/models", surf: "openai-chat", auth: AuthBearer},
		"openai-compatible": {ok: true, local: false, list: "/models", surf: "openai-chat", auth: AuthBearer},
		"anthropic":         {ok: false},
		"bogus":             {ok: false},
	}
	for kind, want := range cases {
		d, ok := Template(kind)
		if ok != want.ok {
			t.Fatalf("Template(%q) ok = %v, want %v", kind, ok, want.ok)
		}
		if !want.ok {
			continue
		}
		if d.Local != want.local {
			t.Fatalf("Template(%q).Local = %v, want %v", kind, d.Local, want.local)
		}
		if d.ListPath != want.list {
			t.Fatalf("Template(%q).ListPath = %q, want %q", kind, d.ListPath, want.list)
		}
		if string(d.Surface) != want.surf {
			t.Fatalf("Template(%q).Surface = %q, want %q", kind, d.Surface, want.surf)
		}
		if d.Auth != want.auth {
			t.Fatalf("Template(%q).Auth = %q, want %q", kind, d.Auth, want.auth)
		}
	}
}
