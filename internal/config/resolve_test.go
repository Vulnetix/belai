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

func TestResolveReadOnlyOrigin(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()

	if err := SaveGlobal(Settings{ReadOnly: boolPtr(true)}); err != nil {
		t.Fatalf("SaveGlobal: %v", err)
	}
	eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if eff.Settings.ReadOnly == nil || !*eff.Settings.ReadOnly {
		t.Fatalf("expected read_only true from global settings")
	}
	if eff.Origin["read_only"] != SourceGlobal {
		t.Fatalf("read_only origin = %q, want global", eff.Origin["read_only"])
	}
}

func TestResolveClassifierPrecedence(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()

	if err := SaveGlobal(Settings{
		Classifier: &ClassifierSettings{Provider: "cloudflare-workers-ai", Model: "global-cls", Effort: "low"},
	}); err != nil {
		t.Fatalf("SaveGlobal: %v", err)
	}

	env := func(k string) string {
		switch k {
		case "SIGNET_CLASSIFIER_MODEL":
			return "env-cls"
		}
		return ""
	}
	flags := Settings{Classifier: &ClassifierSettings{Effort: "high"}}

	eff, err := Resolve(workdir, env, flags)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	cls := eff.Settings.Classifier
	if cls == nil {
		t.Fatal("Classifier should be resolved")
	}
	if cls.Provider != "cloudflare-workers-ai" {
		t.Fatalf("provider = %q, want global cloudflare-workers-ai", cls.Provider)
	}
	if cls.Model != "env-cls" {
		t.Fatalf("model = %q, want env env-cls", cls.Model)
	}
	if cls.Effort != "high" {
		t.Fatalf("effort = %q, want flag high", cls.Effort)
	}
	if eff.Origin["classifier"] != SourceFlag {
		t.Fatalf("classifier origin = %q, want flag", eff.Origin["classifier"])
	}
}

func TestClassifierSettingsIsZero(t *testing.T) {
	if !(&ClassifierSettings{}).IsZero() {
		t.Fatal("empty ClassifierSettings should be zero")
	}
	if (&ClassifierSettings{Model: "m"}).IsZero() {
		t.Fatal("ClassifierSettings with model should not be zero")
	}
}

func TestClassifierChunkDefaults(t *testing.T) {
	if got := (ClassifierChunkSettings{}).MaxBytesOr(); got != 1<<20 {
		t.Fatalf("MaxBytesOr = %d, want 1MiB", got)
	}
	if got := (ClassifierChunkSettings{}).ConcurrencyOr(); got != 4 {
		t.Fatalf("ConcurrencyOr = %d, want 4", got)
	}
	if got := (ClassifierChunkSettings{MaxBytes: 10, Concurrency: 2}).MaxBytesOr(); got != 10 {
		t.Fatalf("MaxBytesOr = %d, want 10", got)
	}
}
