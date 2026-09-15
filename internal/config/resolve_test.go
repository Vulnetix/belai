package config

import (
	"reflect"
	"testing"
)

func ptr[T any](v T) *T { return &v }

func TestResolvePrecedence(t *testing.T) {
	dir := t.TempDir()

	// Write global settings.
	global := Settings{Model: "global-model", Provider: "global-provider", Effort: "low"}
	if err := SaveGlobal(global); err != nil {
		t.Fatalf("SaveGlobal: %v", err)
	}
	// Write project settings.
	proj := Settings{Model: "project-model", Effort: "medium"}
	if err := SaveProject(dir, proj); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}

	eff, err := Resolve(dir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if eff.Settings.Model != "project-model" {
		t.Fatalf("model: want project-model, got %q", eff.Settings.Model)
	}
	if eff.Origin["model"] != SourceProject {
		t.Fatalf("model origin: want project, got %s", eff.Origin["model"])
	}

	if eff.Settings.Provider != "global-provider" {
		t.Fatalf("provider: want global-provider, got %q", eff.Settings.Provider)
	}
	if eff.Origin["provider"] != SourceGlobal {
		t.Fatalf("provider origin: want global, got %s", eff.Origin["provider"])
	}

	if eff.Settings.Effort != "medium" {
		t.Fatalf("effort: want medium, got %q", eff.Settings.Effort)
	}
	if eff.Origin["effort"] != SourceProject {
		t.Fatalf("effort origin: want project, got %s", eff.Origin["effort"])
	}
}

func TestResolveEnvOverrides(t *testing.T) {
	dir := t.TempDir()
	env := func(k string) string {
		switch k {
		case "SIGNET_PROVIDER":
			return "env-provider"
		case "SIGNET_MODEL":
			return "env-model"
		case "SIGNET_EFFORT":
			return "high"
		}
		return ""
	}

	eff, err := Resolve(dir, env, Settings{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if eff.Settings.Provider != "env-provider" {
		t.Fatalf("provider: want env-provider, got %q", eff.Settings.Provider)
	}
	if eff.Origin["provider"] != SourceEnv {
		t.Fatalf("provider origin: want env, got %s", eff.Origin["provider"])
	}
}

func TestResolveFlagOverridesEnv(t *testing.T) {
	dir := t.TempDir()
	env := func(k string) string {
		if k == "SIGNET_MODEL" {
			return "env-model"
		}
		return ""
	}
	flags := Settings{Model: "flag-model"}

	eff, err := Resolve(dir, env, flags)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if eff.Settings.Model != "flag-model" {
		t.Fatalf("model: want flag-model, got %q", eff.Settings.Model)
	}
	if eff.Origin["model"] != SourceFlag {
		t.Fatalf("model origin: want flag, got %s", eff.Origin["model"])
	}
}

func TestResolveCaveman(t *testing.T) {
	dir := t.TempDir()
	flags := Settings{Caveman: ptr(true)}

	eff, err := Resolve(dir, func(string) string { return "" }, flags)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if eff.Settings.Caveman == nil || !*eff.Settings.Caveman {
		t.Fatal("expected caveman true")
	}
	if eff.Origin["caveman"] != SourceFlag {
		t.Fatalf("caveman origin: want flag, got %s", eff.Origin["caveman"])
	}
}

func TestResolvePermissionsMerge(t *testing.T) {
	dir := t.TempDir()
	global := Settings{Permissions: PermissionRules{Allow: []string{"Read"}}}
	if err := SaveGlobal(global); err != nil {
		t.Fatalf("SaveGlobal: %v", err)
	}
	proj := Settings{Permissions: PermissionRules{Deny: []string{"Write"}}}
	if err := SaveProject(dir, proj); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}

	eff, err := Resolve(dir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := PermissionRules{Allow: []string{"Read"}, Deny: []string{"Write"}}
	if !reflect.DeepEqual(eff.Settings.Permissions, want) {
		t.Fatalf("permissions mismatch:\n want=%+v\n  got=%+v", want, eff.Settings.Permissions)
	}
}

func TestResolveUIAndContextWindows(t *testing.T) {
	flags := Settings{
		UI:               &UISettings{Banner: ptr(true)},
		ContextWindows:   map[string]int{"custom": 128000},
		ShowSessionNames: ptr(false),
	}

	eff, err := Resolve(t.TempDir(), func(string) string { return "" }, flags)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if eff.Settings.UI == nil || eff.Settings.UI.Banner == nil || !*eff.Settings.UI.Banner {
		t.Fatal("expected banner true")
	}
	if !reflect.DeepEqual(eff.Settings.ContextWindows, map[string]int{"custom": 128000}) {
		t.Fatalf("context windows mismatch: %v", eff.Settings.ContextWindows)
	}
	if eff.Settings.ShowSessionNames == nil || *eff.Settings.ShowSessionNames {
		t.Fatal("expected show_session_names false")
	}
}
