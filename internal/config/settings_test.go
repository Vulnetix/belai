package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

func TestGlobalSettingsRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	want := Settings{
		Model:       "gpt-5",
		Effort:      "high",
		Caveman:     boolPtr(true),
		Permissions: map[string]string{"bash": "ask", "edit": "allow"},
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
		Model:       "claude-opus-4",
		Effort:      "max",
		Caveman:     boolPtr(false),
		Permissions: map[string]string{"write": "block"},
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
		Permissions: map[string]string{"bash": "ask", "edit": "allow"},
	}
	proj := Settings{
		Model:       "project-model",
		Caveman:     boolPtr(true),
		Permissions: map[string]string{"edit": "block"},
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
		Model:       "project-model",                                   // project wins
		Effort:      "low",                                             // project empty -> global
		Caveman:     boolPtr(true),                                     // project wins
		Permissions: map[string]string{"bash": "ask", "edit": "block"}, // merged
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("merge mismatch:\n want=%+v\n  got=%+v", want, got)
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
