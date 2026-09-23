package config

import (
	"strings"
	"testing"
)

func TestValidateRouting(t *testing.T) {
	valid := Settings{Routing: &RoutingSettings{
		Kind:     RoutingRouted,
		UseCases: map[string]RoutingTarget{"mode_eval": {Provider: "openrouter", Model: "typesafe/jev-1.13"}},
	}}
	if err := ValidateRouting(valid); err != nil {
		t.Fatalf("valid routing rejected: %v", err)
	}

	defined := Settings{Routing: &RoutingSettings{Kind: RoutingDefined}}
	if err := ValidateRouting(defined); err != nil {
		t.Fatalf("defined routing rejected: %v", err)
	}

	empty := Settings{Routing: &RoutingSettings{}}
	if err := ValidateRouting(empty); err != nil {
		t.Fatalf("empty routing rejected: %v", err)
	}

	cases := []struct {
		name string
		r    RoutingSettings
		want string
	}{
		{"bad kind", RoutingSettings{Kind: "bogus"}, "invalid"},
		{"empty key", RoutingSettings{Kind: RoutingRouted, UseCases: map[string]RoutingTarget{"": {Provider: "openai"}}}, "empty use-case key"},
		{"empty target", RoutingSettings{Kind: RoutingRouted, UseCases: map[string]RoutingTarget{"main": {}}}, "must set provider and/or model"},
		{"invalid provider", RoutingSettings{Kind: RoutingRouted, UseCases: map[string]RoutingTarget{"main": {Provider: "not a provider!"}}}, "invalid provider"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateRouting(Settings{Routing: &tc.r})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ValidateRouting(%+v) = %v, want error containing %q", tc.r, err, tc.want)
			}
		})
	}
}

func TestRoutingMergeAndOverride(t *testing.T) {
	global := Settings{Routing: &RoutingSettings{
		Kind:     RoutingRouted,
		UseCases: map[string]RoutingTarget{"main": {Provider: "openai", Model: "gpt-5"}, "mode_eval": {Provider: "openai", Model: "gpt-5-mini"}},
	}}
	proj := Settings{Routing: &RoutingSettings{
		Kind:     RoutingRouted,
		UseCases: map[string]RoutingTarget{"mode_eval": {Provider: "openrouter", Model: "typesafe/jev-1.13"}},
	}}
	out := global.Override(proj)
	if out.Routing == nil {
		t.Fatal("routing not merged")
	}
	if out.Routing.Kind != RoutingRouted {
		t.Fatalf("kind = %q", out.Routing.Kind)
	}
	// main from global survives; mode_eval is replaced by project.
	if out.Routing.UseCases["main"].Model != "gpt-5" {
		t.Fatalf("main use case = %+v", out.Routing.UseCases["main"])
	}
	if out.Routing.UseCases["mode_eval"].Provider != "openrouter" {
		t.Fatalf("mode_eval use case = %+v", out.Routing.UseCases["mode_eval"])
	}
}

func TestRoutingIsZero(t *testing.T) {
	var nilR *RoutingSettings
	if !nilR.IsZero() {
		t.Fatal("nil routing must be zero")
	}
	if !(&RoutingSettings{}).IsZero() {
		t.Fatal("empty routing must be zero")
	}
	if (&RoutingSettings{Kind: RoutingDefined}).IsZero() {
		t.Fatal("defined routing must not be zero")
	}
	if (&RoutingSettings{UseCases: map[string]RoutingTarget{"main": {}}}).IsZero() {
		t.Fatal("routing with use cases must not be zero")
	}
}
