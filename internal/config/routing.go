package config

import (
	"fmt"
	"strings"

	"github.com/vulnetix/signet/internal/provider"
)

// ValidateRouting validates the routing settings. An invalid value fails the
// whole resolve, exactly like an invalid provider profile: a half-applied
// routing table would silently send role-manager traffic to the wrong model.
func ValidateRouting(s Settings) error {
	if c := s.Classifier; c != nil {
		switch c.Tier {
		case "", ClassifierTierMain, ClassifierTierFast:
		default:
			return fmt.Errorf("classifier.tier %q is invalid (want %q or %q)", c.Tier, ClassifierTierMain, ClassifierTierFast)
		}
	}
	if s.Routing == nil {
		return nil
	}
	if f := s.Routing.Fast; f != nil {
		if f.Provider == "" && f.Model == "" {
			return fmt.Errorf("routing.fast_model must set provider and/or model")
		}
		if f.Provider != "" && !provider.Builtin(f.Provider) && !provider.ValidCustomName(f.Provider) {
			return fmt.Errorf("routing.fast_model: invalid provider %q", f.Provider)
		}
	}
	switch s.Routing.Kind {
	case "", RoutingDefined, RoutingRouted:
	default:
		return fmt.Errorf("routing.kind %q is invalid (want %q or %q)", s.Routing.Kind, RoutingDefined, RoutingRouted)
	}
	switch s.Routing.ModeDetection {
	case "", ModeDetectionAuto, ModeDetectionJev, ModeDetectionLlm:
	default:
		return fmt.Errorf("routing.mode_detection %q is invalid (want %q, %q or %q)", s.Routing.ModeDetection, ModeDetectionAuto, ModeDetectionJev, ModeDetectionLlm)
	}
	for key, t := range s.Routing.UseCases {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("routing.use_cases has an empty use-case key")
		}
		if t.Provider == "" && t.Model == "" {
			return fmt.Errorf("routing.use_cases.%s must set provider and/or model", key)
		}
		if t.Provider != "" && !provider.Builtin(t.Provider) && !provider.ValidCustomName(t.Provider) {
			return fmt.Errorf("routing.use_cases.%s: invalid provider %q", key, t.Provider)
		}
	}
	return nil
}
