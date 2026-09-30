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

func TestGoalJudgeThresholdDefaultsAndLimits(t *testing.T) {
	d := DefaultJevThresholds()
	if d.GoalCompleteAt != 0.90 || d.GoalRivalMax != 0.20 || d.GoalNotStartedAt != 0.85 {
		t.Fatalf("goal defaults = %v %v %v", d.GoalCompleteAt, d.GoalRivalMax, d.GoalNotStartedAt)
	}
	for _, c := range []struct {
		name string
		th   JevThresholdSettings
		key  string
	}{
		{"complete below a majority", JevThresholdSettings{GoalCompleteAt: f(0.4)}, "goal_complete_at"},
		{"not started below a majority", JevThresholdSettings{GoalNotStartedAt: f(0.4)}, "goal_not_started_at"},
		{"rival above a half", JevThresholdSettings{GoalRivalMax: f(0.6)}, "goal_rival_max"},
	} {
		th := c.th
		if err := ValidateJev(Settings{Jev: &JevSettings{Thresholds: &th}}); err == nil || !strings.Contains(err.Error(), c.key) {
			t.Errorf("%s: got %v", c.name, err)
		}
	}
	ok := JevThresholdSettings{GoalCompleteAt: f(0.5), GoalRivalMax: f(0.5)}
	if err := ValidateJev(Settings{Jev: &JevSettings{Thresholds: &ok}}); err != nil {
		t.Fatalf("the limits themselves are allowed: %v", err)
	}
}

func TestDeliveryCutoffsDefaultsAndValidation(t *testing.T) {
	d := DefaultJevThresholds()
	if d.ClearAt != 0.50 || d.AlignAt != 0.40 || d.CoverAt != 0.40 {
		t.Fatalf("delivery cut-off defaults %v %v %v", d.ClearAt, d.AlignAt, d.CoverAt)
	}
	keys := JevThresholdKeys()
	for _, k := range []string{"clear_at", "align_at", "cover_at"} {
		found := false
		for _, have := range keys {
			found = found || have == k
		}
		if !found {
			t.Errorf("%s is not a threshold key", k)
		}
	}
	for _, v := range []float64{-0.1, 1.1} {
		x := v
		for _, set := range []*JevThresholdSettings{{ClearAt: &x}, {AlignAt: &x}, {CoverAt: &x}} {
			if err := set.Validate(); err == nil {
				t.Errorf("%v must be refused", v)
			}
		}
	}
	for _, v := range []float64{0, 0.5, 1} {
		x := v
		if err := (&JevThresholdSettings{ClearAt: &x, AlignAt: &x, CoverAt: &x}).Validate(); err != nil {
			t.Errorf("%v is a fair cut-off: %v", v, err)
		}
	}
	// The user's own value takes effect and the others keep their defaults.
	c := 0.7
	r := (&JevThresholdSettings{ClearAt: &c}).Resolved()
	if r.ClearAt != 0.7 || r.AlignAt != 0.40 {
		t.Fatalf("resolved %+v", r)
	}
}
