package config

import (
	"strings"
	"testing"
)

func decisionSettings() Settings {
	return Settings{Classifier: &ClassifierSettings{Provider: "decision-local", Model: "decider-4b"}}
}

func TestJevJobsDefaultOnButNeedABackend(t *testing.T) {
	var none Settings
	for _, j := range JevJobs {
		if !none.JevJobSet(j) {
			t.Errorf("%s switch should default on", j)
		}
		if none.JevJobEnabled(j) {
			t.Errorf("%s must not run without a decision backend", j)
		}
	}
	s := decisionSettings()
	for _, j := range JevJobs {
		if !s.JevJobEnabled(j) {
			t.Errorf("%s should run with a decision backend", j)
		}
	}
}

func TestJevConfiguredRecognisesEveryBackend(t *testing.T) {
	cases := []struct {
		name string
		s    Settings
		want bool
	}{
		{"none", Settings{}, false},
		{"chat classifier", Settings{Classifier: &ClassifierSettings{Provider: "openai", Model: "gpt-x"}}, false},
		{"inherit", Settings{Classifier: &ClassifierSettings{}}, false},
		{"local", decisionSettings(), true},
		{"openrouter jev", Settings{Classifier: &ClassifierSettings{Provider: "openrouter", Model: "typesafe/jev-1.13"}}, true},
		{"openrouter chat", Settings{Classifier: &ClassifierSettings{Provider: "openrouter", Model: "openai/gpt-x"}}, false},
		{"self-hosted", Settings{
			Classifier: &ClassifierSettings{Provider: "myjev", Model: "m"},
			Providers:  map[string]ProviderProfile{"myjev": {Kind: "jev", BaseURL: "https://jev.example.com"}},
		}, true},
		{"chat provider profile", Settings{
			Classifier: &ClassifierSettings{Provider: "myllm", Model: "m"},
			Providers:  map[string]ProviderProfile{"myllm": {Kind: "openai", BaseURL: "https://x.example.com"}},
		}, false},
	}
	for _, c := range cases {
		if got := c.s.JevConfigured(); got != c.want {
			t.Errorf("%s: JevConfigured = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestJevJobSwitchOff(t *testing.T) {
	s := decisionSettings()
	s.Jev = &JevSettings{Jobs: map[string]bool{"bash_swap": false}}
	if s.JevJobEnabled(JevBashSwap) {
		t.Fatal("bash_swap is switched off")
	}
	if s.JevJobSet(JevBashSwap) {
		t.Fatal("the switch itself reads off")
	}
}

func TestValidateJev(t *testing.T) {
	ok := Settings{Jev: &JevSettings{Jobs: map[string]bool{"bash_swap": true}, LocatePreviews: "off"}}
	if err := ValidateJev(ok); err != nil {
		t.Fatal(err)
	}
	if err := ValidateJev(Settings{}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateJev(Settings{Jev: &JevSettings{Jobs: map[string]bool{"nope": true}}}); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("unknown job: %v", err)
	}
	if err := ValidateJev(Settings{Jev: &JevSettings{LocatePreviews: "everywhere"}}); err == nil {
		t.Fatal("bad locate_previews accepted")
	}
}

func TestEveryJobIsValidAndUnique(t *testing.T) {
	seen := map[JevJob]bool{}
	for _, j := range JevJobs {
		if seen[j] || !ValidJevJob(string(j)) {
			t.Errorf("job %q duplicated or invalid", j)
		}
		seen[j] = true
	}
	if len(JevJobs) == 0 {
		t.Error("no Jev jobs listed")
	}
}

func TestProjectLayerCanOnlyTurnJevJobsOff(t *testing.T) {
	user := &JevSettings{Jobs: map[string]bool{"bash_swap": false}, LocatePreviews: LocatePreviewsHosted}
	proj := &JevSettings{Jobs: map[string]bool{"bash_swap": true, "prune_compaction": false}, LocatePreviews: LocatePreviewsHosted}
	got := mergeJev(user, proj, true)
	if got.Jobs["bash_swap"] {
		t.Fatal("a project layer turned a job on")
	}
	if got.Jobs["prune_compaction"] {
		t.Fatal("a project layer could not turn a job off")
	}
	// Widening previews from a project file is ignored; narrowing is taken.
	if got := mergeJev(&JevSettings{LocatePreviews: LocatePreviewsLocal}, &JevSettings{LocatePreviews: LocatePreviewsHosted}, true); got.LocatePreviews != LocatePreviewsLocal {
		t.Fatalf("project widened previews to %q", got.LocatePreviews)
	}
	if got := mergeJev(&JevSettings{LocatePreviews: LocatePreviewsLocal}, &JevSettings{LocatePreviews: LocatePreviewsOff}, true); got.LocatePreviews != LocatePreviewsOff {
		t.Fatalf("project could not narrow previews: %q", got.LocatePreviews)
	}
	// The user layer may do either.
	if got := mergeJev(nil, &JevSettings{Jobs: map[string]bool{"bash_swap": true}, LocatePreviews: LocatePreviewsHosted}, false); !got.Jobs["bash_swap"] || got.LocatePreviews != LocatePreviewsHosted {
		t.Fatalf("user layer merge = %+v", got)
	}
	// The merge never aliases its inputs.
	user.Jobs["bash_swap"] = true
	if got.Jobs["bash_swap"] {
		t.Fatal("merge aliased the user map")
	}
}

func TestLocatePreviewsDefault(t *testing.T) {
	if (Settings{}).LocatePreviewsMode() != LocatePreviewsLocal {
		t.Fatal("default must be local")
	}
	if (Settings{Jev: &JevSettings{LocatePreviews: LocatePreviewsOff}}).LocatePreviewsMode() != LocatePreviewsOff {
		t.Fatal("explicit mode ignored")
	}
}

func TestResolveDropsProjectJevWidening(t *testing.T) {
	e := &Effective{Settings: Settings{Jev: &JevSettings{Jobs: map[string]bool{"bash_swap": false}}}, Origin: map[string]Source{}}
	e.apply(Settings{Jev: &JevSettings{Jobs: map[string]bool{"bash_swap": true}}}, SourceProject)
	if e.Settings.JevJobSet(JevBashSwap) {
		t.Fatal("project layer re-enabled a job")
	}
	e.apply(Settings{Jev: &JevSettings{Jobs: map[string]bool{"bash_swap": true}}}, SourceGlobal)
	if !e.Settings.JevJobSet(JevBashSwap) {
		t.Fatal("global layer could not re-enable a job")
	}
}
