package config

import "testing"

func TestValidateRoutingFastAndTier(t *testing.T) {
	ok := []Settings{
		{},
		{Classifier: &ClassifierSettings{Tier: ClassifierTierFast}},
		{Classifier: &ClassifierSettings{Tier: ClassifierTierMain}},
		{Routing: &RoutingSettings{Fast: &RoutingTarget{Model: "gpt-5-mini"}}},
		{Routing: &RoutingSettings{Fast: &RoutingTarget{Provider: "anthropic"}}},
	}
	for _, s := range ok {
		if err := ValidateRouting(s); err != nil {
			t.Fatalf("%+v: %v", s, err)
		}
	}
	bad := []Settings{
		{Classifier: &ClassifierSettings{Tier: "tiny"}},
		{Routing: &RoutingSettings{Fast: &RoutingTarget{}}},
		{Routing: &RoutingSettings{Fast: &RoutingTarget{Provider: "not a name!"}}},
	}
	for _, s := range bad {
		if err := ValidateRouting(s); err == nil {
			t.Fatalf("%+v: want an error", s)
		}
	}
}

func TestRoutingFastMergesAndCountsAsAnOverride(t *testing.T) {
	var r RoutingSettings
	r.merge(&RoutingSettings{Fast: &RoutingTarget{Provider: "openai", Model: "gpt-5-mini"}})
	if r.Fast == nil || r.Fast.Model != "gpt-5-mini" || r.IsZero() {
		t.Fatalf("merge = %+v", r)
	}
	var c ClassifierSettings
	c.merge(&ClassifierSettings{Tier: ClassifierTierFast})
	if c.Tier != ClassifierTierFast {
		t.Fatalf("tier not merged: %+v", c)
	}
}
