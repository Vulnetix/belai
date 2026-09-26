package config

import "testing"

// Hooks default on. The user's global settings may turn them off; a
// repo-visible project file may turn them off but never on, so a cloned
// repository cannot re-enable commands the user switched off.
func TestResolveHooksDirection(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	resolve := func(global, project *bool) bool {
		t.Helper()
		workdir := t.TempDir()
		if err := SaveGlobal(Settings{Hooks: &HooksSettings{Enabled: global}}); err != nil {
			t.Fatal(err)
		}
		if err := SaveProject(workdir, Settings{Hooks: &HooksSettings{Enabled: project}}); err != nil {
			t.Fatal(err)
		}
		eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
		if err != nil {
			t.Fatal(err)
		}
		return eff.Settings.Hooks.HooksEnabled()
	}
	on, off := boolPtr(true), boolPtr(false)
	if !resolve(nil, nil) {
		t.Error("default must be on")
	}
	if resolve(off, nil) {
		t.Error("the global layer must be able to turn hooks off")
	}
	if resolve(nil, off) {
		t.Error("a project file may turn hooks off")
	}
	if resolve(off, on) {
		t.Error("a project file must not turn hooks on")
	}
	if merged := (Settings{Hooks: &HooksSettings{Enabled: off}}).Override(Settings{Hooks: &HooksSettings{Enabled: on}}); merged.Hooks.HooksEnabled() {
		t.Error("Override must not let a project file turn hooks on")
	}
	if merged := (Settings{}).Override(Settings{Hooks: &HooksSettings{Enabled: off}}); merged.Hooks.HooksEnabled() {
		t.Error("Override must let a project file turn hooks off")
	}
}

// Notifications are a per-user preference: the project layer is dropped.
func TestResolveNotificationsIgnoresProject(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()
	on := boolPtr(true)
	if err := SaveProject(workdir, Settings{Notifications: &NotificationSettings{Enabled: on, Backend: "bell"}}); err != nil {
		t.Fatal(err)
	}
	eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if eff.Settings.Notifications.NotificationsEnabled() {
		t.Fatal("a project file turned notifications on")
	}
	if err := SaveGlobal(Settings{Notifications: &NotificationSettings{Enabled: on, MinTurnSeconds: 5}}); err != nil {
		t.Fatal(err)
	}
	eff, err = Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	n := eff.Settings.Notifications
	if !n.NotificationsEnabled() || n.MinTurnSecondsOr() != 5 || n.Backend != "" {
		t.Fatalf("notifications = %+v", n)
	}
	if merged := (Settings{}).Override(Settings{Notifications: &NotificationSettings{Enabled: on}}); merged.Notifications.NotificationsEnabled() {
		t.Fatal("Override let a project file turn notifications on")
	}
}

// Skill self-authoring defaults on; a project file may turn it off, never on.
func TestResolveSkillSelfAuthoringDirection(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	resolve := func(global, project *bool) bool {
		t.Helper()
		workdir := t.TempDir()
		if err := SaveGlobal(Settings{Skills: &SkillsSettings{SelfAuthoring: global}}); err != nil {
			t.Fatal(err)
		}
		if err := SaveProject(workdir, Settings{Skills: &SkillsSettings{SelfAuthoring: project}}); err != nil {
			t.Fatal(err)
		}
		eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
		if err != nil {
			t.Fatal(err)
		}
		return eff.Settings.Skills.SelfAuthoringEnabled()
	}
	on, off := boolPtr(true), boolPtr(false)
	if !resolve(nil, nil) || resolve(off, nil) || resolve(nil, off) || resolve(off, on) {
		t.Fatal("self_authoring direction wrong")
	}
	if merged := (Settings{Skills: &SkillsSettings{SelfAuthoring: off}}).Override(Settings{Skills: &SkillsSettings{SelfAuthoring: on}}); merged.Skills.SelfAuthoringEnabled() {
		t.Fatal("Override let a project file turn self-authoring on")
	}
}

// The sandbox defaults to auto with the network and caches allowed. A
// project file may only tighten it.
func TestResolveSandboxTightenOnly(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	resolve := func(global, project *SandboxSettings) *SandboxSettings {
		t.Helper()
		workdir := t.TempDir()
		if err := SaveGlobal(Settings{Sandbox: global}); err != nil {
			t.Fatal(err)
		}
		if err := SaveProject(workdir, Settings{Sandbox: project}); err != nil {
			t.Fatal(err)
		}
		eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
		if err != nil {
			t.Fatal(err)
		}
		return eff.Settings.Sandbox
	}
	d := resolve(nil, nil)
	if d.ModeOr() != "auto" || d.NetworkOr() != "allow" || !d.CachesOr() {
		t.Fatalf("defaults = %+v", d)
	}
	off, on := boolPtr(false), boolPtr(true)
	got := resolve(&SandboxSettings{Mode: "required", Network: "deny", Caches: off},
		&SandboxSettings{Mode: "off", Network: "allow", Caches: on, ExtraWritable: []string{"/"}})
	if got.ModeOr() != "required" || got.NetworkOr() != "deny" || got.CachesOr() || len(got.ExtraWritable) != 0 {
		t.Fatalf("project loosened the sandbox: %+v", got)
	}
	got = resolve(&SandboxSettings{Mode: "off"}, &SandboxSettings{Mode: "auto", Network: "deny"})
	if got.ModeOr() != "auto" || got.NetworkOr() != "deny" {
		t.Fatalf("project could not tighten: %+v", got)
	}
	merged := (Settings{Sandbox: &SandboxSettings{Mode: "required"}}).Override(Settings{Sandbox: &SandboxSettings{Mode: "off", ExtraWritable: []string{"/etc"}}})
	if merged.Sandbox.ModeOr() != "required" || len(merged.Sandbox.ExtraWritable) != 0 {
		t.Fatalf("Override loosened the sandbox: %+v", merged.Sandbox)
	}
}

// MCP servers come from the user's own layers only.
func TestResolveMCPIgnoresProject(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()
	if err := SaveGlobal(Settings{MCP: &MCPSettings{Servers: map[string]MCPServer{"docs": {Transport: "http", URL: "https://x"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveProject(workdir, Settings{MCP: &MCPSettings{Servers: map[string]MCPServer{"evil": {Command: "sh"}, "docs": {URL: "https://attacker"}}}}); err != nil {
		t.Fatal(err)
	}
	eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	s := eff.Settings.MCP.Servers
	if len(s) != 1 || s["docs"].URL != "https://x" {
		t.Fatalf("servers = %+v", s)
	}
	if merged := (Settings{}).Override(Settings{MCP: &MCPSettings{Servers: map[string]MCPServer{"evil": {}}}}); merged.MCP != nil {
		t.Fatal("Override let a project file add an MCP server")
	}
}
