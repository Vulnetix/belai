package jev

import (
	"testing"

	"github.com/vulnetix/belai/internal/config"
)

func TestPickTeleport(t *testing.T) {
	t.Cleanup(func() { config.SetActiveJevThresholds(config.DefaultJevThresholds()) })
	config.SetActiveJevThresholds(config.DefaultJevThresholds())

	for _, c := range []struct {
		name   string
		scores map[string]float64
		want   TeleportVerdict
	}{
		{"clearly verified", map[string]float64{"verified": 0.95, "incomplete": 0.10, "failed": 0.05}, TeleportVerified},
		{"verified but a rival is too high", map[string]float64{"verified": 0.95, "incomplete": 0.40, "failed": 0.05}, TeleportUnclear},
		{"clearly failed", map[string]float64{"verified": 0.05, "incomplete": 0.10, "failed": 0.95}, TeleportFailed},
		{"a lead for incomplete is reported as incomplete", map[string]float64{"verified": 0.20, "incomplete": 0.70, "failed": 0.10}, TeleportIncomplete},
		{"nothing clear", map[string]float64{"verified": 0.40, "incomplete": 0.40, "failed": 0.40}, TeleportUnclear},
		{"an option the backend did not answer", map[string]float64{"verified": 0.99, "incomplete": 0.01}, TeleportUnclear},
		{"nothing answered", nil, TeleportUnclear},
	} {
		if got, _ := PickTeleport(c.scores); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	got, pcts := PickTeleport(map[string]float64{"verified": 0.904, "incomplete": 0.2})
	if got != TeleportUnclear || pcts.Verified != 90 || pcts.Incomplete != 20 || pcts.Failed != -1 {
		t.Fatalf("%q %+v: percentages are rounded and an unanswered option is -1", got, pcts)
	}
}

func TestPickTeleportReadsTheUsersCutOffs(t *testing.T) {
	t.Cleanup(func() { config.SetActiveJevThresholds(config.DefaultJevThresholds()) })
	th := config.DefaultJevThresholds()
	th.GoalCompleteAt = 0.99
	config.SetActiveJevThresholds(th)

	if got, _ := PickTeleport(map[string]float64{"verified": 0.95, "incomplete": 0.02, "failed": 0.02}); got != TeleportUnclear {
		t.Fatalf("goal_complete_at 0.99 must refuse 0.95, got %q", got)
	}
}
