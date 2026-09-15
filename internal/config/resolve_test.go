package config

import "testing"

func TestResolvePrecedence(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()

	if err := SaveState(State{Model: "state-model", Provider: "openai"}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	if err := SaveGlobal(Settings{Model: "global-model", Effort: "low"}); err != nil {
		t.Fatalf("SaveGlobal: %v", err)
	}
	if err := SaveProject(workdir, Settings{Model: "project-model", Caveman: boolPtr(true)}); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}

	env := func(k string) string {
		switch k {
		case "SIGNET_MODEL":
			return "env-model"
		case "SIGNET_PROVIDER":
			return "anthropic"
		}
		return ""
	}
	flags := Settings{Model: "flag-model"}

	eff, err := Resolve(workdir, env, flags)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if eff.Settings.Model != "flag-model" {
		t.Fatalf("Model = %q, want flag-model", eff.Settings.Model)
	}
	if eff.Origin["model"] != SourceFlag {
		t.Fatalf("model origin = %q, want flag", eff.Origin["model"])
	}
	if eff.Settings.Provider != "anthropic" {
		t.Fatalf("Provider = %q, want env anthropic", eff.Settings.Provider)
	}
	if eff.Origin["provider"] != SourceEnv {
		t.Fatalf("provider origin = %q, want env", eff.Origin["provider"])
	}
	// effort comes from global (not overridden by anything above it).
	if eff.Settings.Effort != "low" {
		t.Fatalf("Effort = %q, want low from global", eff.Settings.Effort)
	}
	if eff.Origin["effort"] != SourceGlobal {
		t.Fatalf("effort origin = %q, want global", eff.Origin["effort"])
	}
	// caveman comes from project.
	if eff.Settings.Caveman == nil || !*eff.Settings.Caveman {
		t.Fatalf("Caveman should come from project")
	}
	if eff.Origin["caveman"] != SourceProject {
		t.Fatalf("caveman origin = %q, want project", eff.Origin["caveman"])
	}
}

func TestResolveStateBeatsDefaultOnly(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	if err := SaveState(State{Model: "state-model"}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	if err := SaveGlobal(Settings{Model: "global-model"}); err != nil {
		t.Fatalf("SaveGlobal: %v", err)
	}
	eff, err := Resolve(t.TempDir(), func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if eff.Settings.Model != "global-model" {
		t.Fatalf("global should beat state, got %q", eff.Settings.Model)
	}
	if eff.Origin["model"] != SourceGlobal {
		t.Fatalf("origin = %q", eff.Origin["model"])
	}
}

func TestResolveNilEnvFallsBackToOS(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	// nil env must not panic; it falls back to os.Getenv.
	if _, err := Resolve(t.TempDir(), nil, Settings{}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
}

func TestResolveBashReadOnlyOrigin(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()

	if err := SaveGlobal(Settings{BashReadOnly: boolPtr(true)}); err != nil {
		t.Fatalf("SaveGlobal: %v", err)
	}
	eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if eff.Settings.BashReadOnly == nil || !*eff.Settings.BashReadOnly {
		t.Fatalf("expected bash_readonly true from global settings")
	}
	if eff.Origin["bash_readonly"] != SourceGlobal {
		t.Fatalf("bash_readonly origin = %q, want global", eff.Origin["bash_readonly"])
	}
}
