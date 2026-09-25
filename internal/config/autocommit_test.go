package config

import (
	"os"
	"strings"
	"testing"
)

func TestAutoCommitPerTaskDefaultAndRoundTrip(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())

	var zero Settings
	if zero.AutoCommitPerTaskEnabled() {
		t.Fatal("unset auto_commit_per_task must default to off")
	}

	if err := SaveGlobal(Settings{AutoCommitPerTask: boolPtr(true)}); err != nil {
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
	if !strings.Contains(string(data), `"auto_commit_per_task": true`) {
		t.Fatalf("settings file should carry auto_commit_per_task: true, got %s", data)
	}
	got, err := LoadGlobal()
	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}
	if got.AutoCommitPerTask == nil || !*got.AutoCommitPerTask || !got.AutoCommitPerTaskEnabled() {
		t.Fatalf("round-trip = %+v, want auto_commit_per_task true", got)
	}
}

func TestResolveDropsProjectAutoCommitPerTask(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()
	on := true
	if err := SaveProject(workdir, Settings{AutoCommitPerTask: &on}); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}

	eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if eff.Settings.AutoCommitPerTaskEnabled() {
		t.Fatalf("project auto_commit_per_task must be dropped, got %+v", eff.Settings.AutoCommitPerTask)
	}
	if eff.Origin["auto_commit_per_task"] == SourceProject {
		t.Fatalf("project must not claim provenance for auto_commit_per_task")
	}
}

func TestResolveAutoCommitPerTaskGlobalWins(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()
	if err := SaveGlobal(Settings{AutoCommitPerTask: boolPtr(true)}); err != nil {
		t.Fatalf("SaveGlobal: %v", err)
	}
	eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !eff.Settings.AutoCommitPerTaskEnabled() {
		t.Fatal("global auto_commit_per_task must resolve on")
	}
	if eff.Origin["auto_commit_per_task"] != SourceGlobal {
		t.Fatalf("origin = %q, want global", eff.Origin["auto_commit_per_task"])
	}
}
