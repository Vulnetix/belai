package main

import "github.com/vulnetix/belai/internal/config"

// applyNoCheckpointsFlag switches file checkpoints off (docs/checkpoints.md)
// when -no-checkpoints is set. The flag is the user's own choice, as a settings
// entry in their own layers is, and it can only turn the feature off: there is
// no flag that turns it on, and a repository's setting cannot reach here.
func applyNoCheckpointsFlag(s *config.Settings, off bool) {
	if !off {
		return
	}
	c := config.CheckpointSettings{}
	if s.Checkpoints != nil {
		c = *s.Checkpoints
	}
	no := false
	c.Enabled = &no
	s.Checkpoints = &c
}
