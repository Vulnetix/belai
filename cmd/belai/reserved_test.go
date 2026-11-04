package main

import (
	"bytes"
	"flag"
	"path/filepath"
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/fleet"
	"github.com/vulnetix/belai/internal/kanban"
)

// A start that is not the Software Factory's caps at the worker cap less the
// reserved slots; a factory start caps at the full cap.
func TestStartCapHonoursReservedWorkers(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	two := 2
	tightened := config.Settings{Agents: &config.AgentsSettings{MaxWorkers: &two}}
	cases := []struct {
		name     string
		settings config.Settings
		override int
		reserved int
		factory  bool
		want     int
	}{
		{"nothing reserved", config.Settings{}, 28, 0, false, 28},
		{"a console start leaves the reserved slots", config.Settings{}, 28, 4, false, 24},
		{"a factory start takes the full cap", config.Settings{}, 28, 4, true, 28},
		{"the default cap less one reserved", config.Settings{}, 0, 1, false, config.DefaultMaxWorkers - 1},
		{"reservation clamped so one slot stays", config.Settings{}, 3, 9, false, 1},
		{"a repository's lower cap still holds", tightened, 15, 1, false, 1},
		{"a repository's lower cap holds a factory start", tightened, 15, 1, true, 2},
	}
	for _, c := range cases {
		if got := startCap(c.settings, c.override, c.reserved, c.factory); got != c.want {
			t.Errorf("%s: startCap = %d, want %d", c.name, got, c.want)
		}
	}
}

// The flag belai rc passes wins over the setting, even as 0; without it the
// user's agents.factory_reserved_workers applies.
func TestFactoryReserved(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	if got := factoryReserved(-1); got != 0 {
		t.Fatalf("unset = %d, want 0", got)
	}
	n := 3
	if err := config.Mutate(config.ScopeGlobal, "", func(s *config.Settings) error {
		s.Agents = &config.AgentsSettings{FactoryReservedWorkers: &n}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := factoryReserved(-1); got != 3 {
		t.Errorf("setting = %d, want 3", got)
	}
	if got := factoryReserved(0); got != 0 {
		t.Errorf("flag 0 = %d, want 0", got)
	}
	if got := factoryReserved(5); got != 5 {
		t.Errorf("flag 5 = %d, want 5", got)
	}
}

func TestRCReservedOverride(t *testing.T) {
	for _, c := range []struct {
		args []string
		want *int
	}{
		{nil, nil},
		{[]string{"-factory-reserved-workers", "0"}, new(int)},
		{[]string{"-factory-reserved-workers", "4"}, func() *int { n := 4; return &n }()},
	} {
		fs := flag.NewFlagSet("rc", flag.ContinueOnError)
		n := fs.Int("factory-reserved-workers", 0, "")
		if err := fs.Parse(c.args); err != nil {
			t.Fatal(err)
		}
		got := reservedOverride(fs, *n)
		if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
			t.Errorf("%v: reservedOverride = %v, want %v", c.args, got, c.want)
		}
	}
}

// A factory start that joins a running crew marks it as serving the
// factory, a later start never unmarks it, and the reservation follows the
// latest start.
func TestJoinCrewRunTakesTheFactoryMark(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BELAI_HOME", home)
	store := kanban.Open(filepath.Join(home, "kanban"))
	reg := fleet.NewRegistry(filepath.Join(home, "agents", "run"), store)
	dir := t.TempDir()
	run, err := reg.LaunchCrew(fleet.CrewRun{Crew: "c", Scope: []fleet.ScopeDir{{Path: dir}},
		Members: []agentprofile.Member{{Profile: "builder", Replicas: 1}}, ReservedWorkers: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code, err := joinCrewRun(reg, run, []fleet.ScopeDir{{Path: dir}}, crewLaunch{crew: "c", fill: true, factory: true, reserved: 2, stdout: &out}); code != 0 || err != nil {
		t.Fatalf("join: %d, %v", code, err)
	}
	got, _ := reg.GetRun(run.ID)
	if !got.Factory || got.ReservedWorkers != 2 {
		t.Fatalf("after a factory join: %+v", got)
	}
	if code, err := joinCrewRun(reg, got, []fleet.ScopeDir{{Path: dir}}, crewLaunch{crew: "c", fill: true, reserved: 3, stdout: &out}); code != 0 || err != nil {
		t.Fatalf("join: %d, %v", code, err)
	}
	if again, _ := reg.GetRun(run.ID); !again.Factory || again.ReservedWorkers != 3 {
		t.Fatalf("after a console join: %+v", again)
	}
}
