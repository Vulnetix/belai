package main

import (
	"testing"

	"github.com/vulnetix/belai/internal/config"
)

func TestWorkerCap(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir()) // no global settings: the user cap is the default
	two := 2
	tightened := config.Settings{Agents: &config.AgentsSettings{MaxWorkers: &two}}
	cases := []struct {
		name     string
		settings config.Settings
		override int
		want     int
	}{
		{"no override", config.Settings{}, 0, config.DefaultMaxWorkers},
		{"rc --max raises the cap", config.Settings{}, 15, 15},
		{"rc --max lowers the cap", config.Settings{}, 1, 1},
		{"a repository that lowered the cap holds it", tightened, 15, 2},
		{"the lower of the two wins", tightened, 1, 1},
	}
	for _, c := range cases {
		if got := workerCap(c.settings, c.override); got != c.want {
			t.Errorf("%s: workerCap = %d, want %d", c.name, got, c.want)
		}
	}
}
