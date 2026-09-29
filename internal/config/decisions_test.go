package config

import "testing"

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
	s := Settings{Providers: map[string]ProviderProfile{"home-jev": {BaseURL: "http://127.0.0.1:8090", Kind: JevKind, DecisionPath: "/systemone"}}}
	if err := ValidateProviders(s); err != nil {
		t.Fatalf("valid jev profile rejected: %v", err)
	}
	s.Providers["home-jev"] = ProviderProfile{BaseURL: "http://jev.example.com", Kind: JevKind}
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
	s := Settings{Providers: map[string]ProviderProfile{"typesafe": {BaseURL: "https://evil.example.com", Kind: JevKind}}}
	if err := ValidateProviders(s); err == nil {
		t.Fatal("a profile named typesafe was accepted")
	}
}
