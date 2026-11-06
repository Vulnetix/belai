package config

import "testing"

// Fleet workers run unattended: a project file may turn them off, turn
// publishing off and lower the worker cap, never the reverse — on both merge
// paths.
func TestResolveAgentsTightenOnly(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	resolve := func(global, project *AgentsSettings) Settings {
		t.Helper()
		workdir := t.TempDir()
		if err := SaveGlobal(Settings{Agents: global}); err != nil {
			t.Fatal(err)
		}
		if err := SaveProject(workdir, Settings{Agents: project}); err != nil {
			t.Fatal(err)
		}
		eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
		if err != nil {
			t.Fatal(err)
		}
		return eff.Settings
	}
	on, off := boolPtr(true), boolPtr(false)

	s := resolve(nil, nil)
	if !s.AgentsEnabled() || !s.AgentsPublishEnabled() || s.MaxWorkers() != DefaultMaxWorkers {
		t.Fatalf("defaults %+v", s.Agents)
	}
	if s := resolve(nil, &AgentsSettings{Enabled: off, Publish: off, MaxWorkers: intPtr(1)}); s.AgentsEnabled() || s.AgentsPublishEnabled() || s.MaxWorkers() != 1 {
		t.Fatalf("project tightening lost: %+v", s.Agents)
	}
	if s := resolve(&AgentsSettings{Enabled: off, Publish: off, MaxWorkers: intPtr(2)}, &AgentsSettings{Enabled: on, Publish: on, MaxWorkers: intPtr(50)}); s.AgentsEnabled() || s.AgentsPublishEnabled() || s.MaxWorkers() != 2 {
		t.Fatalf("project loosened the fleet: %+v", s.Agents)
	}
	if s := resolve(&AgentsSettings{MaxWorkers: intPtr(8)}, nil); s.MaxWorkers() != 8 {
		t.Fatalf("global cap lost: %d", s.MaxWorkers())
	}

	loose := Settings{Agents: &AgentsSettings{Enabled: on, Publish: on, MaxWorkers: intPtr(50)}}
	if m := (Settings{Agents: &AgentsSettings{Enabled: off, MaxWorkers: intPtr(2)}}).Override(loose); m.AgentsEnabled() || m.MaxWorkers() != 2 {
		t.Fatalf("Override loosened the fleet: %+v", m.Agents)
	}
	if m := (Settings{}).Override(Settings{Agents: &AgentsSettings{Publish: off, MaxWorkers: intPtr(1)}}); m.AgentsPublishEnabled() || m.MaxWorkers() != 1 {
		t.Fatalf("Override lost project tightening: %+v", m.Agents)
	}
}

// agents.clone is on by default; the global layer turns it off, and a project
// file may turn it off but never back on.
func TestResolveAgentsClone(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	resolve := func(global, project *AgentsSettings) Settings {
		t.Helper()
		workdir := t.TempDir()
		if err := SaveGlobal(Settings{Agents: global}); err != nil {
			t.Fatal(err)
		}
		if err := SaveProject(workdir, Settings{Agents: project}); err != nil {
			t.Fatal(err)
		}
		eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
		if err != nil {
			t.Fatal(err)
		}
		return eff.Settings
	}
	on, off := boolPtr(true), boolPtr(false)
	if !(Settings{}).AgentsCloneEnabled() || !resolve(nil, nil).AgentsCloneEnabled() {
		t.Fatal("agents.clone is on by default")
	}
	if resolve(&AgentsSettings{Clone: off}, nil).AgentsCloneEnabled() {
		t.Fatal("the global layer turns agents.clone off")
	}
	if resolve(nil, &AgentsSettings{Clone: off}).AgentsCloneEnabled() {
		t.Fatal("a project may turn agents.clone off")
	}
	if resolve(&AgentsSettings{Clone: off}, &AgentsSettings{Clone: on}).AgentsCloneEnabled() {
		t.Fatal("a project turned agents.clone back on")
	}
}

// agents.factory_reserved_workers is 0 by default, comes from the user's
// layers only, and is clamped below the cap so one slot stays open.
func TestResolveAgentsFactoryReservedWorkers(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	resolve := func(global, project *AgentsSettings) Settings {
		t.Helper()
		workdir := t.TempDir()
		if err := SaveGlobal(Settings{Agents: global}); err != nil {
			t.Fatal(err)
		}
		if err := SaveProject(workdir, Settings{Agents: project}); err != nil {
			t.Fatal(err)
		}
		eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
		if err != nil {
			t.Fatal(err)
		}
		return eff.Settings
	}
	if n := resolve(nil, nil).FactoryReservedWorkers(); n != 0 {
		t.Fatalf("default %d, want 0", n)
	}
	if n := resolve(&AgentsSettings{MaxWorkers: intPtr(28), FactoryReservedWorkers: intPtr(4)}, nil).FactoryReservedWorkers(); n != 4 {
		t.Fatalf("global %d, want 4", n)
	}
	if n := resolve(&AgentsSettings{FactoryReservedWorkers: intPtr(2)}, &AgentsSettings{FactoryReservedWorkers: intPtr(0)}).FactoryReservedWorkers(); n != 2 {
		t.Fatalf("a project changed the reservation: %d", n)
	}
	if n := resolve(&AgentsSettings{MaxWorkers: intPtr(3), FactoryReservedWorkers: intPtr(9)}, nil).FactoryReservedWorkers(); n != 2 {
		t.Fatalf("clamped %d, want 2 under a cap of 3", n)
	}
	s := Settings{Agents: &AgentsSettings{FactoryReservedWorkers: intPtr(9)}}
	if n := s.FactoryReservedWorkersUnder(28); n != 9 {
		t.Fatalf("under 28: %d", n)
	}
	if n := s.FactoryReservedWorkersSetting(); n != 9 {
		t.Fatalf("setting: %d", n)
	}
	if n := (Settings{Agents: &AgentsSettings{FactoryReservedWorkers: intPtr(-3)}}).FactoryReservedWorkersSetting(); n != 0 {
		t.Fatalf("negative: %d", n)
	}
	for _, c := range []struct{ reserved, limit, want int }{{4, 28, 4}, {4, 4, 3}, {1, 1, 0}, {3, 0, 0}, {-1, 8, 0}} {
		if got := ReservedUnder(c.reserved, c.limit); got != c.want {
			t.Errorf("ReservedUnder(%d, %d) = %d, want %d", c.reserved, c.limit, got, c.want)
		}
	}
}

// agents.max_candidates is the user's ceiling on best-of-N attempts: a project
// file may lower it and never raise it, and a value out of range is the default.
func TestResolveAgentsMaxCandidatesTightenOnly(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	resolve := func(global, project *AgentsSettings) Settings {
		t.Helper()
		workdir := t.TempDir()
		if err := SaveGlobal(Settings{Agents: global}); err != nil {
			t.Fatal(err)
		}
		if err := SaveProject(workdir, Settings{Agents: project}); err != nil {
			t.Fatal(err)
		}
		eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
		if err != nil {
			t.Fatal(err)
		}
		return eff.Settings
	}
	if got := resolve(nil, nil).MaxCandidates(); got != DefaultMaxCandidates {
		t.Fatalf("default %d", got)
	}
	if got := resolve(&AgentsSettings{MaxCandidates: intPtr(2)}, nil).MaxCandidates(); got != 2 {
		t.Fatalf("user ceiling lost: %d", got)
	}
	if got := resolve(nil, &AgentsSettings{MaxCandidates: intPtr(1)}).MaxCandidates(); got != 1 {
		t.Fatalf("project lowering lost: %d", got)
	}
	if got := resolve(&AgentsSettings{MaxCandidates: intPtr(2)}, &AgentsSettings{MaxCandidates: intPtr(4)}).MaxCandidates(); got != 2 {
		t.Fatalf("project raised the ceiling: %d", got)
	}
	if got := resolve(&AgentsSettings{MaxCandidates: intPtr(3)}, &AgentsSettings{MaxCandidates: intPtr(0)}).MaxCandidates(); got != 3 {
		t.Fatalf("project value 0 changed the ceiling: %d", got)
	}
	loose := Settings{Agents: &AgentsSettings{MaxCandidates: intPtr(4)}}
	if m := (Settings{Agents: &AgentsSettings{MaxCandidates: intPtr(2)}}).Override(loose); m.MaxCandidates() != 2 {
		t.Fatalf("Override raised the ceiling: %+v", m.Agents)
	}
	for _, bad := range []int{-1, 0, MaxCandidatesLimit + 1} {
		if got := (Settings{Agents: &AgentsSettings{MaxCandidates: intPtr(bad)}}).MaxCandidates(); got != DefaultMaxCandidates {
			t.Fatalf("out-of-range %d gave %d", bad, got)
		}
	}
}
