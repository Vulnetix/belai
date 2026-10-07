package config

import (
	"os"
	"strings"
	"testing"
)

// Hooks default on. The user's global settings may turn them off; a
// repo-visible project file may turn them off but never on, so a cloned
// repository cannot re-enable commands the user switched off.
func TestResolveHooksDirection(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
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
	t.Setenv("BELAI_HOME", t.TempDir())
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
	t.Setenv("BELAI_HOME", t.TempDir())
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
	t.Setenv("BELAI_HOME", t.TempDir())
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
	t.Setenv("BELAI_HOME", t.TempDir())
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

// A repository cannot choose where telemetry goes.
func TestResolveTelemetryIgnoresProject(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()
	if err := SaveProject(workdir, Settings{Telemetry: &TelemetrySettings{OTLPEndpoint: "https://attacker.example"}}); err != nil {
		t.Fatal(err)
	}
	eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if eff.Settings.Telemetry != nil {
		t.Fatalf("project set telemetry: %+v", eff.Settings.Telemetry)
	}
	if merged := (Settings{}).Override(Settings{Telemetry: &TelemetrySettings{OTLPEndpoint: "x"}}); merged.Telemetry != nil {
		t.Fatal("Override let a project file set telemetry")
	}
}

// "events": [] in the user's file means no desktop notifications, not the
// default set.
func TestNotificationEmptyEventsStaysEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BELAI_HOME", home)
	path, err := GlobalSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"notifications":{"enabled":true,"events":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	eff, err := Resolve(t.TempDir(), func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	ev := eff.Settings.Notifications.Events
	if ev == nil || len(ev) != 0 {
		t.Fatalf("events = %#v, want empty non-nil", ev)
	}
}

func TestSandboxValueFallbacks(t *testing.T) {
	for mode, want := range map[string]string{"": "auto", "bogus": "auto", "off": "off", "required": "required"} {
		if got := (&SandboxSettings{Mode: mode}).ModeOr(); got != want {
			t.Errorf("ModeOr(%q) = %q, want %q", mode, got, want)
		}
	}
	for net, want := range map[string]string{"": "allow", "allow": "allow", "deny": "deny", "open": "deny"} {
		if got := (&SandboxSettings{Network: net}).NetworkOr(); got != want {
			t.Errorf("NetworkOr(%q) = %q, want %q", net, got, want)
		}
	}
	var n *NotificationSettings
	if n.NotificationsEnabled() || n.MinTurnSecondsOr() != DefaultNotifyMinTurnSeconds {
		t.Fatal("notification defaults wrong")
	}
	if (&NotificationSettings{MinTurnSeconds: -3}).MinTurnSecondsOr() != DefaultNotifyMinTurnSeconds {
		t.Fatal("negative min_turn_seconds not defaulted")
	}
}

// The kanban board defaults on; a project file may turn it off, never on.
func TestResolveKanbanDirection(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	resolve := func(global, project *bool) bool {
		t.Helper()
		workdir := t.TempDir()
		if err := SaveGlobal(Settings{Kanban: global}); err != nil {
			t.Fatal(err)
		}
		if err := SaveProject(workdir, Settings{Kanban: project}); err != nil {
			t.Fatal(err)
		}
		eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
		if err != nil {
			t.Fatal(err)
		}
		return eff.Settings.KanbanEnabled()
	}
	on, off := boolPtr(true), boolPtr(false)
	if !resolve(nil, nil) {
		t.Error("default must be on")
	}
	if resolve(off, nil) {
		t.Error("the global layer must be able to turn the board off")
	}
	if resolve(nil, off) {
		t.Error("a project file may turn the board off")
	}
	if resolve(off, on) {
		t.Error("a project file must not turn the board on")
	}
	if merged := (Settings{Kanban: off}).Override(Settings{Kanban: on}); merged.KanbanEnabled() {
		t.Error("Override must not let a project file turn the board on")
	}
	if merged := (Settings{}).Override(Settings{Kanban: off}); merged.KanbanEnabled() {
		t.Error("Override must let a project file turn the board off")
	}
}

// An unknown sandbox mode is skipped when layers merge, so the layer below
// stands; on its own it reads as auto.
func TestUnknownSandboxModeDoesNotLowerAValidOne(t *testing.T) {
	merged := (Settings{Sandbox: &SandboxSettings{Mode: "required"}}).Override(Settings{Sandbox: &SandboxSettings{Mode: "strict"}})
	if got := merged.Sandbox.ModeOr(); got != "required" {
		t.Fatalf("an unknown project mode lowered required to %q", got)
	}
	if got := (&SandboxSettings{Mode: "strict"}).ModeOr(); got != "auto" {
		t.Fatalf("an unknown mode alone reads as %q, want auto", got)
	}
}

// hooks.allowed_programs is a user setting: the project layer is dropped with a
// note, a later user layer replaces an earlier one, and enabling hooks in the
// project layer does not carry a list.
func TestResolveAllowedProgramsAreUserOnly(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()
	if err := SaveProject(workdir, Settings{Hooks: &HooksSettings{AllowedPrograms: []string{"curl", "sh"}}}); err != nil {
		t.Fatal(err)
	}
	eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if got := eff.Settings.Hooks.AllowedProgramList(); len(got) != 0 {
		t.Fatalf("a project layer named programs: %v", got)
	}
	noted := false
	for _, n := range eff.Notes {
		noted = noted || strings.Contains(n, "hooks.allowed_programs")
	}
	if !noted {
		t.Errorf("no note about the ignored project list: %v", eff.Notes)
	}
	if err := SaveGlobal(Settings{Hooks: &HooksSettings{AllowedPrograms: []string{"vulnetix", "vulnetix", "jq"}}}); err != nil {
		t.Fatal(err)
	}
	// A project layer that turns hooks off keeps the user's list.
	if err := SaveProject(workdir, Settings{Hooks: &HooksSettings{Enabled: boolPtr(false)}}); err != nil {
		t.Fatal(err)
	}
	eff, err = Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if eff.Settings.Hooks.HooksEnabled() {
		t.Error("the project layer could not turn hooks off")
	}
	if got := strings.Join(eff.Settings.Hooks.AllowedProgramList(), ","); got != "jq,vulnetix" {
		t.Errorf("list = %q", got)
	}
	if eff.Origin["hooks_allowed_programs"] != SourceGlobal {
		t.Errorf("origin = %v", eff.Origin["hooks_allowed_programs"])
	}
	// Override (the project-over-user merge used elsewhere) never adds a program either.
	merged := Settings{Hooks: &HooksSettings{AllowedPrograms: []string{"vulnetix"}}}.Override(Settings{Hooks: &HooksSettings{AllowedPrograms: []string{"sh"}}})
	if got := strings.Join(merged.Hooks.AllowedProgramList(), ","); got != "vulnetix" {
		t.Errorf("Override list = %q", got)
	}
}

func TestValidateHooksAllowedPrograms(t *testing.T) {
	ok := Settings{Hooks: &HooksSettings{AllowedPrograms: []string{"vulnetix", "python3.12", "node-v2", "g++"}}}
	if err := ValidateHooks(ok); err != nil {
		t.Fatalf("good names refused: %v", err)
	}
	for _, bad := range []string{"", "/bin/sh", "a b", "-x", "../x", "a;b", "$HOME", strings.Repeat("a", 65), "~root"} {
		if err := ValidateHooks(Settings{Hooks: &HooksSettings{AllowedPrograms: []string{bad}}}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	many := make([]string, MaxAllowedPrograms+1)
	for i := range many {
		many[i] = "p" + strings.Repeat("x", i%5)
	}
	if err := ValidateHooks(Settings{Hooks: &HooksSettings{AllowedPrograms: many}}); err == nil {
		t.Error("too many programs accepted")
	}
}
