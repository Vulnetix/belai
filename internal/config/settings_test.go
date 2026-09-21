package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

func TestVulnetixAutoFixDefaultsFalse(t *testing.T) {
	if (VulnetixSettings{}).AutoFixEnabled() {
		t.Fatal("AutoFix must default false")
	}
	trueVal := true
	if !(VulnetixSettings{AutoFix: &trueVal}).AutoFixEnabled() {
		t.Fatal("AutoFixEnabled should honour the opt-in")
	}
}

func TestGlobalSettingsRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	want := Settings{
		Model:       "gpt-5",
		Effort:      "high",
		Caveman:     boolPtr(true),
		Permissions: PermissionRules{Allow: []string{"Read"}, Ask: []string{"Bash"}, Deny: []string{"Write"}},
	}
	if err := SaveGlobal(want); err != nil {
		t.Fatalf("SaveGlobal: %v", err)
	}
	got, err := LoadGlobal()
	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("round-trip mismatch:\n want=%+v\n  got=%+v", want, got)
	}
}

func TestProjectSettingsRoundTrip(t *testing.T) {
	workdir := t.TempDir()

	want := Settings{
		Model:       "claude-opus-4-5",
		Effort:      "max",
		Caveman:     boolPtr(false),
		Permissions: PermissionRules{Deny: []string{"Write(*)"}},
	}
	if err := SaveProject(workdir, want); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}
	got, err := LoadProject(workdir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("round-trip mismatch:\n want=%+v\n  got=%+v", want, got)
	}
}

func TestProjectOverridesGlobal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workdir := t.TempDir()

	global := Settings{
		Model:       "global-model",
		Effort:      "low",
		Caveman:     boolPtr(false),
		Permissions: PermissionRules{Allow: []string{"Read"}, Deny: []string{"Bash(git push*)"}},
	}
	proj := Settings{
		Model:       "project-model",
		Caveman:     boolPtr(true),
		Permissions: PermissionRules{Allow: []string{"Bash(git diff:*)"}},
	}
	if err := SaveGlobal(global); err != nil {
		t.Fatalf("SaveGlobal: %v", err)
	}
	if err := SaveProject(workdir, proj); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}

	got, err := LoadMerged(workdir)
	if err != nil {
		t.Fatalf("LoadMerged: %v", err)
	}

	want := Settings{
		Model:   "project-model", // project wins
		Effort:  "low",           // project empty -> global
		Caveman: boolPtr(true),   // project wins
		// Permissions merge is a union: the global deny and allow survive.
		Permissions: PermissionRules{
			Allow: []string{"Read", "Bash(git diff:*)"},
			Deny:  []string{"Bash(git push*)"},
		},
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("merge mismatch:\n want=%+v\n  got=%+v", want, got)
	}
}

func TestReadOnlyRoundTripAndDefault(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())

	// Default: unset means the full tool set (read-only is an opt-in).
	var zero Settings
	if zero.ReadOnlyEnabled() {
		t.Fatalf("unset read_only should default to full tools")
	}

	// Marshal: key name and value round-trip.
	if err := SaveGlobal(Settings{ReadOnly: boolPtr(true)}); err != nil {
		t.Fatalf("SaveGlobal: %v", err)
	}
	path, err := GlobalSettingsPath()
	if err != nil {
		t.Fatalf("GlobalSettingsPath: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if !strings.Contains(string(data), `"read_only": true`) {
		t.Fatalf("settings file should carry read_only: true, got %s", data)
	}
	got, err := LoadGlobal()
	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}
	if got.ReadOnly == nil || !*got.ReadOnly || !got.ReadOnlyEnabled() {
		t.Fatalf("round-trip = %+v, want read_only true", got)
	}

	// omitempty: an unset value must not be written.
	if err := SaveGlobal(Settings{Model: "m"}); err != nil {
		t.Fatalf("SaveGlobal: %v", err)
	}
	data, _ = os.ReadFile(path)
	if strings.Contains(string(data), "read_only") || strings.Contains(string(data), "bash_readonly") {
		t.Fatalf("unset read_only should be omitted, got %s", data)
	}
}

// TestBashReadOnlyAliasDecodes pins the deprecated alias: a legacy settings
// file carrying bash_readonly still turns the master switch on.
func TestBashReadOnlyAliasDecodes(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	path, err := GlobalSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"bash_readonly": true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if !got.ReadOnlyEnabled() {
		t.Fatal("bash_readonly alias should fold into read_only")
	}
	if got.BashReadOnly != nil {
		t.Fatalf("alias must be cleared after decode, got %+v", got.BashReadOnly)
	}
	if got.ReadOnly == nil || !*got.ReadOnly {
		t.Fatalf("read_only = %+v", got.ReadOnly)
	}
}

// A file carrying both keys resolves to the canonical one: the alias is only
// consulted when read_only is absent, so a migrated file that kept its old key
// around cannot flip the switch back.
func TestReadOnlyWinsOverBashReadOnlyAlias(t *testing.T) {
	var s Settings
	if err := json.Unmarshal([]byte(`{"read_only": false, "bash_readonly": true}`), &s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if s.ReadOnly == nil || *s.ReadOnly {
		t.Fatalf("read_only = %+v, want an explicit false", s.ReadOnly)
	}
	if s.BashReadOnly != nil {
		t.Fatalf("alias must be cleared after decode, got %+v", s.BashReadOnly)
	}
	if s.ReadOnlyEnabled() {
		t.Fatal("ReadOnlyEnabled should follow the canonical key")
	}
}

func TestReadOnlyOverridePrecedence(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()

	// An explicit project false must beat a global true.
	if err := SaveGlobal(Settings{ReadOnly: boolPtr(true)}); err != nil {
		t.Fatalf("SaveGlobal: %v", err)
	}
	if err := SaveProject(workdir, Settings{ReadOnly: boolPtr(false)}); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}
	merged, err := LoadMerged(workdir)
	if err != nil {
		t.Fatalf("LoadMerged: %v", err)
	}
	if merged.ReadOnly == nil || *merged.ReadOnly {
		t.Fatalf("project false should beat global true, got %+v", merged.ReadOnly)
	}

	// An unset project field falls back to the global value.
	if err := SaveProject(workdir, Settings{Model: "m"}); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}
	merged, err = LoadMerged(workdir)
	if err != nil {
		t.Fatalf("LoadMerged: %v", err)
	}
	if merged.ReadOnly == nil || !*merged.ReadOnly {
		t.Fatalf("unset project should fall back to global true, got %+v", merged.ReadOnly)
	}
}

func TestMissingSettingsYieldZero(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	g, err := LoadGlobal()
	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}
	if !reflect.DeepEqual(Settings{}, g) {
		t.Fatalf("expected zero settings, got %+v", g)
	}

	p, err := LoadProject(t.TempDir())
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if !reflect.DeepEqual(Settings{}, p) {
		t.Fatalf("expected zero settings, got %+v", p)
	}
}

func TestStateRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	want := State{Model: "claude-opus-4", Effort: "max", LastMode: "plan"}
	if err := SaveState(want); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	got, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("round-trip mismatch:\n want=%+v\n  got=%+v", want, got)
	}
}

func TestMissingStateYieldsZero(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	st, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !reflect.DeepEqual(State{}, st) {
		t.Fatalf("expected zero state, got %+v", st)
	}
}

func TestProjectPaths(t *testing.T) {
	workdir := "/tmp/work"

	if got := ProjectSettingsPath(workdir); got != "/tmp/work/.vulnetix/settings.json" {
		t.Fatalf("ProjectSettingsPath = %q", got)
	}
	if got := ProjectPlansDir(workdir); got != "/tmp/work/.vulnetix/plans" {
		t.Fatalf("ProjectPlansDir = %q", got)
	}
	if got := ProjectGoalsDir(workdir); got != "/tmp/work/.vulnetix/goals" {
		t.Fatalf("ProjectGoalsDir = %q", got)
	}
	if got := ProjectSignetDir(workdir); got != "/tmp/work/.vulnetix/signet" {
		t.Fatalf("ProjectSignetDir = %q", got)
	}
	if got := ProjectCredentialsPath(workdir); got != "/tmp/work/.vulnetix/signet/credentials.json" {
		t.Fatalf("ProjectCredentialsPath = %q", got)
	}
}

func TestGlobalDirHonoursSignetHome(t *testing.T) {
	t.Setenv("SIGNET_HOME", "/custom/signet")
	got, err := GlobalDir()
	if err != nil {
		t.Fatalf("GlobalDir: %v", err)
	}
	if got != "/custom/signet" {
		t.Fatalf("GlobalDir = %q, want /custom/signet", got)
	}
}

func TestMigrateMovesLegacyDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	legacy := filepath.Join(tmp, ".signet")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "settings.json"), []byte(`{"model":"m"}`), 0o600); err != nil {
		t.Fatalf("write legacy file: %v", err)
	}

	migrated, err := Migrate()
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if !migrated {
		t.Fatalf("expected migration")
	}

	newDir := filepath.Join(tmp, ".vulnetix", "signet")
	if _, err := os.Stat(newDir); err != nil {
		t.Fatalf("new dir missing: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(newDir, "settings.json"))
	if err != nil {
		t.Fatalf("read migrated file: %v", err)
	}
	if string(data) != `{"model":"m"}` {
		t.Fatalf("migrated content wrong: %s", data)
	}
}

func TestMigrateSkipsWhenTargetExists(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	legacy := filepath.Join(tmp, ".signet")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	newDir := filepath.Join(tmp, ".vulnetix", "signet")
	if err := os.MkdirAll(newDir, 0o700); err != nil {
		t.Fatalf("mkdir new: %v", err)
	}

	migrated, err := Migrate()
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if migrated {
		t.Fatalf("expected no migration when target exists")
	}
}

func TestMigrateIsNoOpWithoutLegacyDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	migrated, err := Migrate()
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if migrated {
		t.Fatalf("expected no migration without legacy dir")
	}
}

func TestResilienceOverrideTakesMinimumAttempts(t *testing.T) {
	global := Settings{Resilience: &ResilienceSettings{MaxAttempts: 5, MaxIterations: 12}}
	projectHigh := Settings{Resilience: &ResilienceSettings{MaxAttempts: 10, MaxIterations: 20}}
	got := global.Override(projectHigh)
	if got.Resilience.MaxAttempts != 5 {
		t.Fatalf("project cannot raise attempts: got %d", got.Resilience.MaxAttempts)
	}
	if got.Resilience.MaxIterations != 12 {
		t.Fatalf("project cannot raise iterations: got %d", got.Resilience.MaxIterations)
	}

	projectLow := Settings{Resilience: &ResilienceSettings{MaxAttempts: 2, MaxIterations: 3}}
	got = global.Override(projectLow)
	if got.Resilience.MaxAttempts != 2 {
		t.Fatalf("project can lower attempts: got %d", got.Resilience.MaxAttempts)
	}
	if got.Resilience.MaxIterations != 3 {
		t.Fatalf("project can lower iterations: got %d", got.Resilience.MaxIterations)
	}
}

func TestResilienceOverrideTakesMinimumClarifyRounds(t *testing.T) {
	global := Settings{Resilience: &ResilienceSettings{MaxClarifyRounds: 3}}

	got := global.Override(Settings{Resilience: &ResilienceSettings{MaxClarifyRounds: 5}})
	if got.Resilience.MaxClarifyRounds != 3 {
		t.Fatalf("project cannot raise clarify rounds: got %d", got.Resilience.MaxClarifyRounds)
	}

	got = global.Override(Settings{Resilience: &ResilienceSettings{MaxClarifyRounds: 1}})
	if got.Resilience.MaxClarifyRounds != 1 {
		t.Fatalf("project can lower clarify rounds: got %d", got.Resilience.MaxClarifyRounds)
	}

	got = Settings{Resilience: &ResilienceSettings{}}.Override(Settings{Resilience: &ResilienceSettings{MaxClarifyRounds: 2}})
	if got.Resilience.MaxClarifyRounds != 2 {
		t.Fatalf("unset global should take project clarify rounds: got %d", got.Resilience.MaxClarifyRounds)
	}
}

func TestMaxClarifyRoundsOrDefaults(t *testing.T) {
	var nilSettings *ResilienceSettings
	if got := nilSettings.MaxClarifyRoundsOr(3); got != 3 {
		t.Fatalf("nil MaxClarifyRoundsOr = %d, want 3", got)
	}
	if got := (&ResilienceSettings{}).MaxClarifyRoundsOr(3); got != 3 {
		t.Fatalf("unset MaxClarifyRoundsOr = %d, want 3", got)
	}
	if got := (&ResilienceSettings{MaxClarifyRounds: 5}).MaxClarifyRoundsOr(3); got != 5 {
		t.Fatalf("MaxClarifyRoundsOr = %d, want 5", got)
	}
	if got := (&ResilienceSettings{MaxClarifyRounds: -1}).MaxClarifyRoundsOr(3); got != -1 {
		t.Fatalf("negative MaxClarifyRoundsOr = %d, want -1", got)
	}
}

func TestMaxExploreIterationsOrDefaults(t *testing.T) {
	var nilSettings *ResilienceSettings
	if got := nilSettings.MaxExploreIterationsOr(8); got != 8 {
		t.Fatalf("nil MaxExploreIterationsOr = %d, want 8", got)
	}
	if got := (&ResilienceSettings{}).MaxExploreIterationsOr(8); got != 8 {
		t.Fatalf("unset MaxExploreIterationsOr = %d, want 8", got)
	}
	if got := (&ResilienceSettings{MaxExploreIterations: 12}).MaxExploreIterationsOr(8); got != 12 {
		t.Fatalf("MaxExploreIterationsOr = %d, want 12", got)
	}
}

func TestMaxAgentsOrDefaults(t *testing.T) {
	var nilSettings *ResilienceSettings
	if got := nilSettings.MaxAgentsOr(3); got != 3 {
		t.Fatalf("nil MaxAgentsOr = %d, want 3", got)
	}
	if got := (&ResilienceSettings{}).MaxAgentsOr(3); got != 3 {
		t.Fatalf("unset MaxAgentsOr = %d, want 3", got)
	}
	if got := (&ResilienceSettings{MaxAgents: 1}).MaxAgentsOr(3); got != 1 {
		t.Fatalf("MaxAgentsOr = %d, want 1", got)
	}
}

func TestResilienceOverrideTakesMinimumMaxAgents(t *testing.T) {
	global := Settings{Resilience: &ResilienceSettings{MaxAgents: 3}}
	got := global.Override(Settings{Resilience: &ResilienceSettings{MaxAgents: 10}})
	if got.Resilience.MaxAgents != 3 {
		t.Fatalf("project cannot raise max_agents: got %d", got.Resilience.MaxAgents)
	}
	got = global.Override(Settings{Resilience: &ResilienceSettings{MaxAgents: 1}})
	if got.Resilience.MaxAgents != 1 {
		t.Fatalf("project can lower max_agents: got %d", got.Resilience.MaxAgents)
	}
	got = Settings{Resilience: &ResilienceSettings{}}.Override(Settings{Resilience: &ResilienceSettings{MaxAgents: 2}})
	if got.Resilience.MaxAgents != 2 {
		t.Fatalf("unset global should take project max_agents: got %d", got.Resilience.MaxAgents)
	}
}

func TestPlanExploreEnabledTriState(t *testing.T) {
	if got := (Settings{}).PlanExploreEnabled(); !got {
		t.Fatal("unset plan_explore must default on")
	}
	if got := (Settings{Resilience: &ResilienceSettings{}}).PlanExploreEnabled(); !got {
		t.Fatal("empty resilience must default plan_explore on")
	}
	f := false
	if got := (Settings{Resilience: &ResilienceSettings{PlanExplore: &f}}).PlanExploreEnabled(); got {
		t.Fatal("explicit false must be honoured")
	}
	tr := true
	if got := (Settings{Resilience: &ResilienceSettings{PlanExplore: &tr}}).PlanExploreEnabled(); !got {
		t.Fatal("explicit true must be honoured")
	}
}

func TestPlanExploreOverridePrecedence(t *testing.T) {
	// Project overrides global (last wins), like the UI tri-state toggles.
	f := false
	global := Settings{Resilience: &ResilienceSettings{PlanExplore: &f}}
	got := global.Override(Settings{})
	if got.Resilience.PlanExplore == nil || *got.Resilience.PlanExplore {
		t.Fatalf("project absent must keep global false: got %+v", got.Resilience.PlanExplore)
	}

	tr := true
	got = global.Override(Settings{Resilience: &ResilienceSettings{PlanExplore: &tr}})
	if got.Resilience.PlanExplore == nil || !*got.Resilience.PlanExplore {
		t.Fatal("project true must override global false")
	}

	got = Settings{}.Override(Settings{Resilience: &ResilienceSettings{PlanExplore: &f}})
	if got.Resilience.PlanExplore == nil || *got.Resilience.PlanExplore {
		t.Fatal("project false must override global default")
	}
}

func TestResilienceOverrideTakesMinimumExploreIterations(t *testing.T) {
	global := Settings{Resilience: &ResilienceSettings{MaxExploreIterations: 8}}
	got := global.Override(Settings{Resilience: &ResilienceSettings{MaxExploreIterations: 20}})
	if got.Resilience.MaxExploreIterations != 8 {
		t.Fatalf("project cannot raise explore iterations: got %d", got.Resilience.MaxExploreIterations)
	}
	got = global.Override(Settings{Resilience: &ResilienceSettings{MaxExploreIterations: 4}})
	if got.Resilience.MaxExploreIterations != 4 {
		t.Fatalf("project can lower explore iterations: got %d", got.Resilience.MaxExploreIterations)
	}
	got = Settings{Resilience: &ResilienceSettings{}}.Override(Settings{Resilience: &ResilienceSettings{MaxExploreIterations: 6}})
	if got.Resilience.MaxExploreIterations != 6 {
		t.Fatalf("unset global should take project explore iterations: got %d", got.Resilience.MaxExploreIterations)
	}
}

func TestResilienceOverrideFillsFromGlobal(t *testing.T) {
	global := Settings{Resilience: &ResilienceSettings{MaxAttempts: 4}}
	project := Settings{Resilience: &ResilienceSettings{MaxIterations: 6}}
	got := global.Override(project)
	if got.Resilience.MaxAttempts != 4 || got.Resilience.MaxIterations != 6 {
		t.Fatalf("got %+v", got.Resilience)
	}
}

func TestMaxPassesOrDefaultsToUnbounded(t *testing.T) {
	var nilSettings *ResilienceSettings
	if got := nilSettings.MaxPassesOr(); got != 0 {
		t.Fatalf("nil resilience MaxPassesOr() = %d, want 0 (unbounded)", got)
	}
	if got := (&ResilienceSettings{}).MaxPassesOr(); got != 0 {
		t.Fatalf("unset MaxPassesOr() = %d, want 0 (unbounded)", got)
	}
	if got := (&ResilienceSettings{MaxPasses: 4}).MaxPassesOr(); got != 4 {
		t.Fatalf("MaxPassesOr() = %d, want 4", got)
	}
}

func TestResilienceOverrideTakesMinimumPasses(t *testing.T) {
	global := Settings{Resilience: &ResilienceSettings{MaxPasses: 6}}

	// A project may lower the ceiling.
	got := global.Override(Settings{Resilience: &ResilienceSettings{MaxPasses: 2}})
	if got.Resilience.MaxPasses != 2 {
		t.Fatalf("project cannot lower passes: got %d", got.Resilience.MaxPasses)
	}

	// It may not raise it.
	got = global.Override(Settings{Resilience: &ResilienceSettings{MaxPasses: 99}})
	if got.Resilience.MaxPasses != 6 {
		t.Fatalf("project raised passes: got %d", got.Resilience.MaxPasses)
	}

	// An unset global takes the project value: there is no ceiling to lower.
	got = Settings{Resilience: &ResilienceSettings{}}.Override(Settings{Resilience: &ResilienceSettings{MaxPasses: 3}})
	if got.Resilience.MaxPasses != 3 {
		t.Fatalf("unset global did not take the project ceiling: got %d", got.Resilience.MaxPasses)
	}
}

func TestTodosVisibleDefaultsOn(t *testing.T) {
	if !(Settings{}).TodosVisible() {
		t.Fatal("todo panel must default to visible")
	}
	if !(Settings{UI: &UISettings{}}).TodosVisible() {
		t.Fatal("unset ui.show_todos must default to visible")
	}

	off := false
	if (Settings{UI: &UISettings{ShowTodos: &off}}).TodosVisible() {
		t.Fatal("ui.show_todos=false must hide the panel")
	}
	on := true
	if !(Settings{UI: &UISettings{ShowTodos: &on}}).TodosVisible() {
		t.Fatal("ui.show_todos=true must show the panel")
	}
}

func TestUIOverrideCarriesShowEdits(t *testing.T) {
	off := false
	base := Settings{UI: &UISettings{}}
	got := base.Override(Settings{UI: &UISettings{ShowEdits: &off}})

	if got.EditsVisible() {
		t.Fatal("project ui.show_edits=false did not override")
	}
	if base.UI.ShowEdits != nil {
		t.Fatal("Override mutated the receiver")
	}
}

func TestUIOverrideCarriesShowTodos(t *testing.T) {
	off := false
	base := Settings{UI: &UISettings{}}
	got := base.Override(Settings{UI: &UISettings{ShowTodos: &off}})

	if got.TodosVisible() {
		t.Fatal("project ui.show_todos=false did not override")
	}
	if base.UI.ShowTodos != nil {
		t.Fatal("Override mutated the receiver")
	}
}

func TestCavemanEnabledDefaults(t *testing.T) {
	var zero Settings
	if zero.CavemanEnabled() {
		t.Fatal("unset caveman should default to off")
	}
	on := true
	onSettings := Settings{Caveman: &on}
	if !onSettings.CavemanEnabled() {
		t.Fatal("caveman=true should be enabled")
	}
	off := false
	offSettings := Settings{Caveman: &off}
	if offSettings.CavemanEnabled() {
		t.Fatal("caveman=false should be disabled")
	}
}

func TestFirewallEnabledDefaultsOff(t *testing.T) {
	if (Settings{}).FirewallEnabled() {
		t.Fatal("firewall must default off")
	}
	on := true
	if !(Settings{Vulnetix: &VulnetixSettings{FirewallEnabled: &on}}).FirewallEnabled() {
		t.Fatal("firewall=true must be enabled")
	}
}

func TestFirewallProjectMayTurnOffNeverOn(t *testing.T) {
	on := true
	off := false
	global := Settings{Vulnetix: &VulnetixSettings{FirewallEnabled: &on}}

	// Project turning it off must win.
	got := global.Override(Settings{Vulnetix: &VulnetixSettings{FirewallEnabled: &off}})
	if got.FirewallEnabled() {
		t.Fatal("project settings must be able to turn the firewall off")
	}

	// Project turning it on must not win.
	got = (Settings{}).Override(Settings{Vulnetix: &VulnetixSettings{FirewallEnabled: &on}})
	if got.FirewallEnabled() {
		t.Fatal("project settings must not be able to turn the firewall on")
	}
}

func TestCatalogWindow(t *testing.T) {
	s := Settings{Providers: map[string]ProviderProfile{
		"cloudflare-ai-gateway": {Models: []ProviderModel{
			{ID: "@cf/example/model-a", ContextWindow: 262_144},
			{ID: "@cf/example/model-b"},
		}},
	}}
	if got := s.CatalogWindow("cloudflare-ai-gateway", "@cf/example/model-a"); got != 262_144 {
		t.Fatalf("CatalogWindow = %d, want 262144", got)
	}
	if got := s.CatalogWindow("cloudflare-ai-gateway", "@cf/example/model-b"); got != 0 {
		t.Fatalf("a model with no declared window = %d, want 0", got)
	}
	if got := s.CatalogWindow("cloudflare-ai-gateway", "@cf/example/absent"); got != 0 {
		t.Fatalf("an unlisted model = %d, want 0", got)
	}
	if got := s.CatalogWindow("other", "@cf/example/model-a"); got != 0 {
		t.Fatalf("an unlisted provider = %d, want 0", got)
	}
	if got := (Settings{}).CatalogWindow("p", "m"); got != 0 {
		t.Fatalf("zero settings = %d, want 0", got)
	}
}
