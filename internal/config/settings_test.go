package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

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

func TestBashReadOnlyRoundTripAndDefault(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())

	// Default: unset means full shell (read-only is an opt-in).
	var zero Settings
	if zero.BashReadOnlyEnabled() {
		t.Fatalf("unset bash_readonly should default to full shell")
	}

	// Marshal: key name and value round-trip.
	if err := SaveGlobal(Settings{BashReadOnly: boolPtr(true)}); err != nil {
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
	if !strings.Contains(string(data), `"bash_readonly": true`) {
		t.Fatalf("settings file should carry bash_readonly: true, got %s", data)
	}
	got, err := LoadGlobal()
	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}
	if got.BashReadOnly == nil || !*got.BashReadOnly || !got.BashReadOnlyEnabled() {
		t.Fatalf("round-trip = %+v, want bash_readonly true", got)
	}

	// omitempty: an unset value must not be written.
	if err := SaveGlobal(Settings{Model: "m"}); err != nil {
		t.Fatalf("SaveGlobal: %v", err)
	}
	data, _ = os.ReadFile(path)
	if strings.Contains(string(data), "bash_readonly") {
		t.Fatalf("unset bash_readonly should be omitted, got %s", data)
	}
}

func TestBashReadOnlyOverridePrecedence(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()

	// An explicit project false must beat a global true.
	if err := SaveGlobal(Settings{BashReadOnly: boolPtr(true)}); err != nil {
		t.Fatalf("SaveGlobal: %v", err)
	}
	if err := SaveProject(workdir, Settings{BashReadOnly: boolPtr(false)}); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}
	merged, err := LoadMerged(workdir)
	if err != nil {
		t.Fatalf("LoadMerged: %v", err)
	}
	if merged.BashReadOnly == nil || *merged.BashReadOnly {
		t.Fatalf("project false should beat global true, got %+v", merged.BashReadOnly)
	}

	// An unset project field falls back to the global value.
	if err := SaveProject(workdir, Settings{Model: "m"}); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}
	merged, err = LoadMerged(workdir)
	if err != nil {
		t.Fatalf("LoadMerged: %v", err)
	}
	if merged.BashReadOnly == nil || !*merged.BashReadOnly {
		t.Fatalf("unset project should fall back to global true, got %+v", merged.BashReadOnly)
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
