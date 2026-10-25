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
