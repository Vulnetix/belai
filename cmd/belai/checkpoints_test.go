package main

import (
	"testing"

	"github.com/vulnetix/belai/internal/config"
)

func TestApplyNoCheckpointsFlag(t *testing.T) {
	var s config.Settings
	applyNoCheckpointsFlag(&s, false)
	if !s.CheckpointsEnabled() || s.Checkpoints != nil {
		t.Fatal("no flag must change nothing")
	}
	applyNoCheckpointsFlag(&s, true)
	if s.CheckpointsEnabled() {
		t.Fatal("-no-checkpoints did not switch checkpoints off")
	}

	// It keeps the user's numbers, and an absent flag never switches them back on.
	days := 21
	s = config.Settings{Checkpoints: &config.CheckpointSettings{RetainDays: &days}}
	applyNoCheckpointsFlag(&s, true)
	if s.CheckpointsEnabled() || s.CheckpointsRetainDays() != 21 {
		t.Fatalf("settings = %+v", s.Checkpoints)
	}
	applyNoCheckpointsFlag(&s, false)
	if s.CheckpointsEnabled() {
		t.Fatal("an absent flag switched checkpoints back on")
	}
}

// The flag is a user choice that reaches the effective settings from the flag
// layer, ahead of anything a repository says.
func TestNoCheckpointsFlagWinsOverTheUsersSettings(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	var flags config.Settings
	applyNoCheckpointsFlag(&flags, true)
	eff, err := config.Resolve(t.TempDir(), func(string) string { return "" }, flags)
	if err != nil {
		t.Fatal(err)
	}
	if eff.Settings.CheckpointsEnabled() {
		t.Fatal("-no-checkpoints did not reach the effective settings")
	}
}
