package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidJevURL(t *testing.T) {
	for _, ok := range []string{"https://jev.example.com", "http://127.0.0.1:8090", "http://localhost:8000", "http://[::1]:9000"} {
		if err := ValidJevURL(ok); err != nil {
			t.Errorf("%s rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"http://jev.example.com", "ftp://x", "https://user:pw@jev.example.com", "jev.example.com", ""} {
		if err := ValidJevURL(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestValidDecisionPath(t *testing.T) {
	for _, ok := range []string{"", "/v1/systemone", "/systemone"} {
		if !ValidDecisionPath(ok) {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"v1/systemone", "/../etc", "/a?b", "/a b", "/a#b"} {
		if ValidDecisionPath(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestJevProfileValidation(t *testing.T) {
	s := Settings{Providers: map[string]ProviderProfile{"home-jev": {BaseURL: "http://127.0.0.1:8090", Kind: SystemOneKind, DecisionPath: "/systemone"}}}
	if err := ValidateProviders(s); err != nil {
		t.Fatalf("valid jev profile rejected: %v", err)
	}
	s.Providers["home-jev"] = ProviderProfile{BaseURL: "http://jev.example.com", Kind: SystemOneKind}
	if err := ValidateProviders(s); err == nil {
		t.Fatal("plain http off loopback accepted")
	}
}

func TestRoutingMergeKeepsModeDetection(t *testing.T) {
	base := &RoutingSettings{Kind: RoutingRouted}
	base.merge(&RoutingSettings{ModeDetection: ModeDetectionJev})
	if base.ModeDetection != ModeDetectionJev {
		t.Fatalf("mode detection dropped in merge: %+v", base)
	}
	if (&RoutingSettings{ModeDetection: ModeDetectionLlm}).IsZero() {
		t.Fatal("a mode-detection-only block is not zero")
	}
}

func TestClassifierDecisionMerge(t *testing.T) {
	c := &ClassifierSettings{}
	c.merge(&ClassifierSettings{Decision: ClassifierDecisionSettings{TimeoutMS: 9000, MaxStateBytes: 4096}})
	if c.Decision.TimeoutMS != 9000 || c.Decision.MaxStateBytes != 4096 || c.IsZero() {
		t.Fatalf("decision knobs not merged: %+v", c.Decision)
	}
}

// typesafe is a built-in decision provider: a custom profile may not take
// its name, or a repository could redirect the hosted key.
func TestTypeSafeNameIsReserved(t *testing.T) {
	s := Settings{Providers: map[string]ProviderProfile{"typesafe": {BaseURL: "https://evil.example.com", Kind: SystemOneKind}}}
	if err := ValidateProviders(s); err == nil {
		t.Fatal("a profile named typesafe was accepted")
	}
}

// "jev" is systemone's name before the rename: a settings file that says it
// loads, validates and saves as systemone, for a provider profile and for
// classifier.kind.
func TestLegacyJevKindLoadsAsSystemOne(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BELAI_HOME", home)
	path, err := GlobalSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := `{"providers":{"home-jev":{"base_url":"http://127.0.0.1:8090","kind":"jev"}},"classifier":{"kind":"jev","provider":"home-jev"}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if s.Providers["home-jev"].Kind != SystemOneKind || s.Classifier.Kind != SystemOneKind {
		t.Fatalf("loaded kinds = %q, %q", s.Providers["home-jev"].Kind, s.Classifier.Kind)
	}
	if err := ValidateProviders(s); err != nil {
		t.Fatalf("a legacy profile no longer validates: %v", err)
	}
	if err := Mutate(ScopeGlobal, "", func(*Settings) error { return nil }); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), `"jev"`) || strings.Count(string(b), `"systemone"`) != 2 {
		t.Fatalf("saved file = %s, want both kinds written as systemone", b)
	}
	if CanonicalKind("ollama") != "ollama" || CanonicalKind("") != "" {
		t.Error("CanonicalKind changed a kind that is not the legacy one")
	}
}

func TestStrandsDeciderNameIsReserved(t *testing.T) {
	s := Settings{Providers: map[string]ProviderProfile{"strands-decider": {BaseURL: "http://127.0.0.1:9", Kind: SystemOneKind}}}
	if err := ValidateProviders(s); err == nil {
		t.Fatal("a profile may not take the built-in strands-decider name")
	}
}
