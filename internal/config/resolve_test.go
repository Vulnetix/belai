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
	// A caveman-only block must not report zero: Effective.apply gates the
	// whole block on IsZero, so a zero verdict would drop the setting.
	if (&ClassifierSettings{Caveman: boolPtr(false)}).IsZero() {
		t.Fatal("ClassifierSettings with caveman false should not be zero")
	}
	if (&ClassifierSettings{Caveman: boolPtr(true)}).IsZero() {
		t.Fatal("ClassifierSettings with caveman true should not be zero")
	}
	if (&ClassifierSettings{Kind: "models"}).IsZero() {
		t.Fatal("ClassifierSettings with kind should not be zero")
	}
	if (&ClassifierSettings{Phase1: ClassifierPhaseSettings{Model: "m"}}).IsZero() {
		t.Fatal("ClassifierSettings with phase1 model should not be zero")
	}
	if (&ClassifierSettings{Phase2: ClassifierPhaseSettings{Threshold: 0.7}}).IsZero() {
		t.Fatal("ClassifierSettings with phase2 threshold should not be zero")
	}
}

func TestClassifierPhaseMerge(t *testing.T) {
	base := &ClassifierSettings{
		Kind:   "llm",
		Phase1: ClassifierPhaseSettings{Model: "a", Source: "embedded", Threshold: 0.5},
		Phase2: ClassifierPhaseSettings{Model: "b", Source: "disabled"},
	}
	base.merge(&ClassifierSettings{
		Kind:   "models",
		Phase1: ClassifierPhaseSettings{Model: "a2", Threshold: 0.8},
		Phase2: ClassifierPhaseSettings{Source: "huggingface"},
	})
	if base.Kind != "models" {
		t.Fatalf("kind = %q, want models", base.Kind)
	}
	if base.Phase1.Model != "a2" || base.Phase1.Source != "embedded" || base.Phase1.Threshold != 0.8 {
		t.Fatalf("phase1 = %+v, want model a2 source embedded threshold 0.8", base.Phase1)
	}
	if base.Phase2.Model != "b" || base.Phase2.Source != "huggingface" {
		t.Fatalf("phase2 = %+v, want model b source huggingface", base.Phase2)
	}
}

func TestClassifierCavemanMergeAndAccessor(t *testing.T) {
	cases := []struct {
		name string
		base *ClassifierSettings
		from *ClassifierSettings
		want *bool
	}{
		{"nil over nil", &ClassifierSettings{}, &ClassifierSettings{}, nil},
		{"true over nil", &ClassifierSettings{}, &ClassifierSettings{Caveman: boolPtr(true)}, boolPtr(true)},
		{"false over true", &ClassifierSettings{Caveman: boolPtr(true)}, &ClassifierSettings{Caveman: boolPtr(false)}, boolPtr(false)},
		{"nil keeps true", &ClassifierSettings{Caveman: boolPtr(true)}, &ClassifierSettings{Model: "m"}, boolPtr(true)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.base.merge(tc.from)
			got := tc.base.Caveman
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("caveman = %v, want nil", *got)
			case tc.want != nil && got == nil:
				t.Fatalf("caveman = nil, want %v", *tc.want)
			case tc.want != nil && *got != *tc.want:
				t.Fatalf("caveman = %v, want %v", *got, *tc.want)
			}
			s := Settings{Classifier: tc.base}
			want := tc.want != nil && *tc.want
			if got := s.ClassifierCavemanEnabled(); got != want {
				t.Fatalf("ClassifierCavemanEnabled = %v, want %v", got, want)
			}
		})
	}
	if (Settings{}).ClassifierCavemanEnabled() {
		t.Fatal("no classifier block should mean caveman off")
	}
	if (Settings{Classifier: &ClassifierSettings{}}).ClassifierCavemanEnabled() {
		t.Fatal("nil caveman should mean caveman off")
	}
}

func TestResolveClassifierCavemanEnv(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()

	env := func(k string) string {
		if k == "SIGNET_CLASSIFIER_CAVEMAN" {
			return "true"
		}
		return ""
	}
	eff, err := Resolve(workdir, env, Settings{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !eff.Settings.ClassifierCavemanEnabled() {
		t.Fatal("SIGNET_CLASSIFIER_CAVEMAN=true should enable classifier caveman")
	}
	if eff.Origin["classifier"] != SourceEnv {
		t.Fatalf("classifier origin = %q, want env", eff.Origin["classifier"])
	}

	// An unset or unparseable variable must not claim provenance.
	for _, val := range []string{"", "yes-ish"} {
		eff, err := Resolve(workdir, func(k string) string {
			if k == "SIGNET_CLASSIFIER_CAVEMAN" {
				return val
			}
			return ""
		}, Settings{})
		if err != nil {
			t.Fatalf("Resolve(%q): %v", val, err)
		}
		if eff.Settings.ClassifierCavemanEnabled() {
			t.Fatalf("SIGNET_CLASSIFIER_CAVEMAN=%q enabled caveman", val)
		}
		if _, ok := eff.Origin["classifier"]; ok {
			t.Fatalf("SIGNET_CLASSIFIER_CAVEMAN=%q claimed classifier provenance", val)
		}
	}
}

// The per-project user prefs may relax the posture gates; the repo-visible
// project layer may only tighten. The firewall is the mirror image: prefs may
// turn it on, the project file may not.
func TestResolveProjectPrefsGatesDirection(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	off := false

	// Prefs relax both gates and the resolved origin names the layer.
	workdir := t.TempDir()
	if err := MutateProjectPrefs(workdir, func(p *ProjectPrefs) {
		p.Guardrails = &off
		p.AskPermission = &off
	}); err != nil {
		t.Fatalf("MutateProjectPrefs: %v", err)
	}
	eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if eff.Settings.GuardrailsEnabled() {
		t.Fatal("project prefs guardrails=false must resolve false")
	}
	if eff.Origin["guardrails"] != SourceProjectPrefs {
		t.Fatalf("guardrails origin = %q, want project_prefs", eff.Origin["guardrails"])
	}
	if eff.Settings.AskPermissionEnabled() {
		t.Fatal("project prefs ask_permission=false must resolve false")
	}
	if eff.Origin["ask_permission"] != SourceProjectPrefs {
		t.Fatalf("ask_permission origin = %q, want project_prefs", eff.Origin["ask_permission"])
	}

	// The identical keys in .vulnetix/settings.json are ignored: a cloned repo
	// must not be able to disable the gates.
	projDir := t.TempDir()
	if err := SaveProject(projDir, Settings{Guardrails: &off, AskPermission: &off}); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}
	eff, err = Resolve(projDir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !eff.Settings.GuardrailsEnabled() || !eff.Settings.AskPermissionEnabled() {
		t.Fatal("project settings must not be able to relax the gates")
	}

	// Firewall is inverted: prefs may turn it on.
	on := true
	prefDir := t.TempDir()
	if err := MutateProjectPrefs(prefDir, func(p *ProjectPrefs) { p.FirewallEnabled = &on }); err != nil {
		t.Fatalf("MutateProjectPrefs: %v", err)
	}
	eff, err = Resolve(prefDir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !eff.Settings.FirewallEnabled() {
		t.Fatal("project prefs firewall_enabled=true must resolve true")
	}
	if eff.Origin["firewall_enabled"] != SourceProjectPrefs {
		t.Fatalf("firewall_enabled origin = %q, want project_prefs", eff.Origin["firewall_enabled"])
	}

	// The project file may not turn the firewall on.
	projDir2 := t.TempDir()
	if err := SaveProject(projDir2, Settings{Vulnetix: &VulnetixSettings{FirewallEnabled: &on}}); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}
	eff, err = Resolve(projDir2, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if eff.Settings.FirewallEnabled() {
		t.Fatal("project settings must not be able to enable the firewall")
	}
}

// Env and CLI flags outrank the prefs layer, which is what makes the toggle's
// honesty rule able to name a winning source instead of silently not sticking.
func TestResolveProjectPrefsLoseToEnvAndFlag(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()
	off := false
	if err := MutateProjectPrefs(workdir, func(p *ProjectPrefs) { p.Guardrails = &off }); err != nil {
		t.Fatalf("MutateProjectPrefs: %v", err)
	}

	eff, err := Resolve(workdir, func(k string) string {
		if k == "SIGNET_GUARDRAILS" {
			return "true"
		}
		return ""
	}, Settings{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !eff.Settings.GuardrailsEnabled() {
		t.Fatal("env guardrails=true must beat the prefs false")
	}
	if eff.Origin["guardrails"] != SourceEnv {
		t.Fatalf("guardrails origin = %q, want env", eff.Origin["guardrails"])
	}

	eff, err = Resolve(workdir, func(string) string { return "" }, Settings{Guardrails: boolPtr(true)})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !eff.Settings.GuardrailsEnabled() {
		t.Fatal("flag guardrails=true must beat the prefs false")
	}
	if eff.Origin["guardrails"] != SourceFlag {
		t.Fatalf("guardrails origin = %q, want flag", eff.Origin["guardrails"])
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

func TestResolveResilienceMaxAgents(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()

	// Safety budgets remain tighten-only; MaxAgents is a performance preference
	// and a project may raise it above the global value.
	if err := SaveGlobal(Settings{Resilience: &ResilienceSettings{
		MaxAttempts: 3,
		MaxAgents:   3,
	}}); err != nil {
		t.Fatalf("SaveGlobal: %v", err)
	}
	if err := SaveProject(workdir, Settings{Resilience: &ResilienceSettings{
		MaxAttempts: 10,
		MaxAgents:   15,
	}}); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}

	eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if eff.Settings.Resilience == nil {
		t.Fatal("Resilience settings were not resolved")
	}
	if eff.Settings.Resilience.MaxAttempts != 3 {
		t.Fatalf("MaxAttempts = %d, want 3 (tighten-only)", eff.Settings.Resilience.MaxAttempts)
	}
	if eff.Settings.Resilience.MaxAgents != 15 {
		t.Fatalf("MaxAgents = %d, want 15 (project override)", eff.Settings.Resilience.MaxAgents)
	}
	if eff.Origin["resilience"] != SourceProject {
		t.Fatalf("resilience origin = %q, want project", eff.Origin["resilience"])
	}
}

// The dependency hook defaults on. The user's global settings may turn it
// off; a repo-visible project file may turn it on but never off, so a cloned
// repository cannot silence the check on the dependencies it adds.
func TestResolveDepWatchDirection(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	resolve := func(global, project *bool) bool {
		t.Helper()
		workdir := t.TempDir()
		if err := SaveGlobal(Settings{Vulnetix: &VulnetixSettings{DepWatch: global}}); err != nil {
			t.Fatal(err)
		}
		if err := SaveProject(workdir, Settings{Vulnetix: &VulnetixSettings{DepWatch: project}}); err != nil {
			t.Fatal(err)
		}
		eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
		if err != nil {
			t.Fatal(err)
		}
		return eff.Settings.Vulnetix.DepWatchEnabled()
	}
	on, off := boolPtr(true), boolPtr(false)
	if !resolve(nil, nil) {
		t.Error("default must be on")
	}
	if resolve(off, nil) {
		t.Error("the global layer must be able to turn it off")
	}
	if !resolve(nil, off) {
		t.Error("a project file must not turn it off")
	}
	if !resolve(off, on) {
		t.Error("a project file may turn it on")
	}
	if merged := (Settings{}).Override(Settings{Vulnetix: &VulnetixSettings{DepWatch: off}}); !merged.Vulnetix.DepWatchEnabled() {
		t.Error("Override must not let a project file turn it off")
	}
}
