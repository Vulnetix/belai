package main

import (
	"flag"
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

func TestRCWorkerOverride(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"neither flag keeps agents.max_workers", nil, 0},
		{"an explicit --max is also the worker cap", []string{"-max", "5"}, 5},
		{"--max-workers alone", []string{"-max-workers", "20"}, 20},
		{"--max-workers wins over --max", []string{"-max", "1", "-max-workers", "20"}, 20},
	}
	for _, c := range cases {
		fs := flag.NewFlagSet("rc", flag.ContinueOnError)
		max := fs.Int("max", 3, "")
		maxWorkers := fs.Int("max-workers", 0, "")
		if err := fs.Parse(c.args); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := workerOverride(fs, *max, *maxWorkers); got != c.want {
			t.Errorf("%s: workerOverride = %d, want %d", c.name, got, c.want)
		}
	}
}
