package jev

import (
	"testing"

	"github.com/vulnetix/belai/internal/config"
)

func withThresholds(t *testing.T, th config.JevThresholds) {
	t.Helper()
	config.SetActiveJevThresholds(th)
	t.Cleanup(func() { config.SetActiveJevThresholds(config.DefaultJevThresholds()) })
}

func TestDefaultsMatchConfig(t *testing.T) {
	d := config.DefaultJevThresholds()
	if d.DropAt != DropAt || d.KeepAt != KeepAt || d.StrongAt != StrongAt || d.SwapAt != SwapAt || d.TriageAt != TriageAt {
		t.Fatalf("exported defaults drifted from config.DefaultJevThresholds: %+v", d)
	}
}

func TestThresholdFollowsSettings(t *testing.T) {
	if got := Threshold(0.75); got != Inconclusive {
		t.Fatalf("default: 0.75 is inconclusive, got %v", got)
	}
	th := config.DefaultJevThresholds()
	th.DenyAt, th.AllowAt = 0.7, 0.2
	withThresholds(t, th)
	if got := Threshold(0.75); got != Deny {
		t.Fatalf("0.75 must deny at deny_at 0.7, got %v", got)
	}
	if got := Threshold(0.2); got != Allow {
		t.Fatalf("0.2 must allow at allow_at 0.2 (inclusive), got %v", got)
	}
}

func TestSelectRouteFollowsSettings(t *testing.T) {
	scores := map[string]float64{"a": 0.6, "b": 0.2}
	if SelectRoute(scores) != "a" {
		t.Fatal("default route_at 0.5 must pick a")
	}
	th := config.DefaultJevThresholds()
	th.RouteAt = 0.7
	withThresholds(t, th)
	if got := SelectRoute(scores); got != "" {
		t.Fatalf("route_at 0.7 must fall back, got %q", got)
	}
}
