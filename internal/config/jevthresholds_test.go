package config

import (
	"strings"
	"testing"
)

func f(v float64) *float64 { return &v }

func TestJevThresholdsDefaultsAndOverride(t *testing.T) {
	if got := (Settings{}).JevThresholds(); got != DefaultJevThresholds() {
		t.Fatalf("unset settings must give the defaults, got %+v", got)
	}
	s := Settings{Jev: &JevSettings{Thresholds: &JevThresholdSettings{DenyAt: f(0.7), SwapAt: f(0.99)}}}
	got := s.JevThresholds()
	if got.DenyAt != 0.7 || got.SwapAt != 0.99 || got.AllowAt != 0.10 {
		t.Fatalf("override not applied on top of defaults: %+v", got)
	}
}

func TestValidateJevThresholds(t *testing.T) {
	bad := map[string]*JevThresholdSettings{
		"between 0 and 1": {KeepAt: f(1.5)},
		"at most 0.5":     {AllowAt: f(0.6), DenyAt: f(0.95)},
		"at least 0.5":    {DenyAt: f(0.4)},
		"by at least":     {AllowAt: f(0.48), DenyAt: f(0.5)},
		"drop_at":         {DropAt: f(0.6)},
		"keep_at":         {KeepAt: f(0.9)},
		"lead_at":         {LeadAt: f(0.9)},
	}
	for want, th := range bad {
		err := ValidateJev(Settings{Jev: &JevSettings{Thresholds: th}})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want error containing %q, got %v", want, err)
		}
	}
	ok := &JevThresholdSettings{AllowAt: f(0.2), DenyAt: f(0.75), KeepAt: f(0.6)}
	if err := ValidateJev(Settings{Jev: &JevSettings{Thresholds: ok}}); err != nil {
		t.Fatal(err)
	}
}

func TestMergeJevProjectLayerCannotSetThresholds(t *testing.T) {
	proj := &JevSettings{Thresholds: &JevThresholdSettings{DenyAt: f(0.55)}}
	if got := mergeJev(nil, proj, true); got.Thresholds != nil {
		t.Fatalf("project thresholds must be dropped, got %+v", got.Thresholds)
	}
	user := &JevSettings{Thresholds: &JevThresholdSettings{DenyAt: f(0.8)}}
	got := mergeJev(user, proj, true)
	if got.Thresholds.Resolved().DenyAt != 0.8 {
		t.Fatalf("project layer overrode the user's threshold: %+v", got.Thresholds.Resolved())
	}
	both := mergeJev(user, &JevSettings{Thresholds: &JevThresholdSettings{SwapAt: f(0.9)}}, false)
	if r := both.Thresholds.Resolved(); r.DenyAt != 0.8 || r.SwapAt != 0.9 {
		t.Fatalf("user layers must overlay key by key: %+v", r)
	}
}

func TestSimpleAtDefaultsAndNeedsAMajority(t *testing.T) {
	if DefaultJevThresholds().SimpleAt != 0.80 {
		t.Fatalf("simple_at default = %v", DefaultJevThresholds().SimpleAt)
	}
	err := ValidateJev(Settings{Jev: &JevSettings{Thresholds: &JevThresholdSettings{SimpleAt: f(0.4)}}})
	if err == nil || !strings.Contains(err.Error(), "simple_at") {
		t.Fatalf("simple_at 0.4 must be refused, got %v", err)
	}
	if err := ValidateJev(Settings{Jev: &JevSettings{Thresholds: &JevThresholdSettings{SimpleAt: f(0.5)}}}); err != nil {
		t.Fatal(err)
	}
}
