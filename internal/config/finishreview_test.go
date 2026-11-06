package config

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

func TestFinishReviewDefaults(t *testing.T) {
	for _, s := range []Settings{{}, {FinishReview: &FinishReviewSettings{}}} {
		if !s.FinishReviewEnabled() || s.FinishReviewMaxRounds() != 2 || len(s.FinishReviewScratchPatterns()) != 0 {
			t.Fatalf("defaults wrong for %+v", s.FinishReview)
		}
	}
	var parsed Settings
	if err := json.Unmarshal([]byte(`{"finish_review":{"enabled":false,"max_rounds":4,"scratch_patterns":["*.dump"]}}`), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.FinishReviewEnabled() || parsed.FinishReviewMaxRounds() != 4 || len(parsed.FinishReviewScratchPatterns()) != 1 {
		t.Fatalf("parsed %+v", parsed.FinishReview)
	}
}

func TestValidateFinishReview(t *testing.T) {
	bad := []FinishReviewSettings{
		{MaxRounds: ip(-1)},
		{MaxRounds: ip(6)},
		{ScratchPatterns: []string{""}},
		{ScratchPatterns: []string{"*"}},
		{ScratchPatterns: []string{"**"}},
		{ScratchPatterns: []string{"/etc/*.conf"}},
		{ScratchPatterns: []string{"../x/*.go"}},
		{ScratchPatterns: []string{"a b"}},
		{ScratchPatterns: []string{"a[b"}},
		{ScratchPatterns: []string{"x\ny"}},
		{ScratchPatterns: []string{strings.Repeat("a", 81)}},
		{ScratchPatterns: make([]string, MaxFinishReviewPatterns+1)},
	}
	for i, f := range bad {
		f := f
		if err := ValidateFinishReview(Settings{FinishReview: &f}); err == nil {
			t.Errorf("case %d accepted: %+v", i, f)
		}
	}
	good := FinishReviewSettings{MaxRounds: ip(0), ScratchPatterns: []string{"*.dump", "tmp/*.out", "notes?.md", "a/b/c.txt"}}
	if err := ValidateFinishReview(Settings{FinishReview: &good}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateFinishReview(Settings{}); err != nil {
		t.Fatal(err)
	}
	// An out-of-range value that slipped past validation never reaches the loop.
	if (Settings{FinishReview: &FinishReviewSettings{MaxRounds: ip(99)}}).FinishReviewMaxRounds() != DefaultFinishReviewMaxRounds {
		t.Fatal("an out-of-range max_rounds was used")
	}
	if got := (Settings{FinishReview: &FinishReviewSettings{ScratchPatterns: []string{"ok.*", "bad pattern", "ok.*"}}}).FinishReviewScratchPatterns(); len(got) != 1 {
		t.Fatalf("patterns = %v, want the one valid pattern once", got)
	}
}

// The project layer can only tighten: it cannot switch the gate off or lower
// the rounds, it can raise them and add patterns, and it can remove nothing.
func TestFinishReviewProjectOnlyTightens(t *testing.T) {
	global := Settings{FinishReview: &FinishReviewSettings{MaxRounds: ip(3), ScratchPatterns: []string{"*.dump"}}}

	off := Settings{FinishReview: &FinishReviewSettings{Enabled: bp(false), MaxRounds: ip(0)}}
	got := global.Override(off)
	if !got.FinishReviewEnabled() || got.FinishReviewMaxRounds() != 3 {
		t.Fatalf("a project loosened the gate: %+v", got.FinishReview)
	}
	up := Settings{FinishReview: &FinishReviewSettings{MaxRounds: ip(5), ScratchPatterns: []string{"*.out", "bad pattern", "*.dump"}}}
	got = global.Override(up)
	if got.FinishReviewMaxRounds() != 5 {
		t.Fatalf("a project could not raise the rounds: %d", got.FinishReviewMaxRounds())
	}
	if p := got.FinishReviewScratchPatterns(); len(p) != 2 || p[0] != "*.dump" || p[1] != "*.out" {
		t.Fatalf("patterns = %v, want the user's then the project's valid one", p)
	}
	// An out-of-range project value is dropped.
	if got := global.Override(Settings{FinishReview: &FinishReviewSettings{MaxRounds: ip(99)}}); got.FinishReviewMaxRounds() != 3 {
		t.Fatalf("an out-of-range project value was applied: %d", got.FinishReviewMaxRounds())
	}
	// A project can add patterns where the user set none, and fill only up to the cap.
	var many []string
	for i := 0; i < MaxFinishReviewPatterns+10; i++ {
		many = append(many, "p"+strings.Repeat("x", i%20)+string(rune('a'+i%26))+".tmp"+strings.Repeat("y", i/26))
	}
	got = (Settings{}).Override(Settings{FinishReview: &FinishReviewSettings{ScratchPatterns: many}})
	if len(got.FinishReviewScratchPatterns()) > MaxFinishReviewPatterns {
		t.Fatalf("pattern cap exceeded: %d", len(got.FinishReviewScratchPatterns()))
	}
	// A user layer sets any key, including switching it off.
	user := mergeFinishReview(nil, &FinishReviewSettings{Enabled: bp(false), MaxRounds: ip(0)}, false)
	if (Settings{FinishReview: user}).FinishReviewEnabled() || (Settings{FinishReview: user}).FinishReviewMaxRounds() != 0 {
		t.Fatalf("a user layer could not switch it off: %+v", user)
	}
	// Nothing set anywhere stays nil, so no provenance is claimed for it.
	if mergeFinishReview(nil, &FinishReviewSettings{}, false) != nil {
		t.Fatal("an empty layer produced a block")
	}
}

func TestResolveFinishReviewLayers(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()
	resolve := func(env map[string]string, flags Settings) Effective {
		t.Helper()
		eff, err := Resolve(workdir, func(k string) string { return env[k] }, flags)
		if err != nil {
			t.Fatal(err)
		}
		return eff
	}
	if err := SaveProject(workdir, Settings{FinishReview: &FinishReviewSettings{Enabled: bp(false), MaxRounds: ip(1), ScratchPatterns: []string{"*.dump"}}}); err != nil {
		t.Fatal(err)
	}
	eff := resolve(nil, Settings{})
	if !eff.Settings.FinishReviewEnabled() {
		t.Fatal("a project file switched the gate off")
	}
	noted := false
	for _, n := range eff.Notes {
		if strings.Contains(n, "finish_review.enabled") && strings.Contains(n, "finish_review.max_rounds") {
			noted = true
		}
	}
	if !noted {
		t.Fatalf("no note for the ignored project keys: %v", eff.Notes)
	}
	if p := eff.Settings.FinishReviewScratchPatterns(); len(p) != 1 || eff.Origin["finish_review"] != SourceProject {
		t.Fatalf("project pattern not added: %v %v", p, eff.Origin["finish_review"])
	}

	// The environment and a flag switch it off; neither can switch it on.
	if resolve(map[string]string{"BELAI_FINISH_REVIEW": "0"}, Settings{}).Settings.FinishReviewEnabled() {
		t.Fatal("BELAI_FINISH_REVIEW=0 did not switch the gate off")
	}
	if resolve(map[string]string{"BELAI_FINISH_REVIEW": "false"}, Settings{}).Settings.FinishReviewEnabled() {
		t.Fatal("BELAI_FINISH_REVIEW=false did not switch the gate off")
	}
	if !resolve(map[string]string{"BELAI_FINISH_REVIEW": "banana"}, Settings{}).Settings.FinishReviewEnabled() {
		t.Fatal("an unreadable value switched the gate off")
	}
	if resolve(nil, Settings{FinishReview: &FinishReviewSettings{Enabled: bp(false)}}).Settings.FinishReviewEnabled() {
		t.Fatal("a flag did not switch the gate off")
	}
	if err := SaveGlobal(Settings{FinishReview: &FinishReviewSettings{Enabled: bp(false)}}); err != nil {
		t.Fatal(err)
	}
	if resolve(map[string]string{"BELAI_FINISH_REVIEW": "1"}, Settings{}).Settings.FinishReviewEnabled() {
		t.Fatal("the environment switched on a gate the user turned off")
	}

	// A user layer with a bad value fails the load.
	if err := SaveGlobal(Settings{FinishReview: &FinishReviewSettings{MaxRounds: ip(9)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(workdir, func(string) string { return "" }, Settings{}); err == nil {
		t.Fatal("an out-of-range user value was accepted")
	}
	if err := ValidateSettings(Settings{FinishReview: &FinishReviewSettings{MaxRounds: ip(9)}}); err == nil {
		t.Fatal("ValidateSettings skips finish_review")
	}
}

func TestFinishReviewFileStaysInTheStateDirectory(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	dir, err := FinishReviewDir()
	if err != nil {
		t.Fatal(err)
	}
	p, err := FinishReviewFile("0a1b2c-D_3")
	if err != nil || filepath.Dir(p) != dir || filepath.Ext(p) != ".json" {
		t.Fatalf("path = %q, err = %v", p, err)
	}
	for _, id := range []string{"", "../x", "a/b", ".hidden", "a b", strings.Repeat("a", 65)} {
		if _, err := FinishReviewFile(id); err == nil {
			t.Errorf("goal id %q was accepted", id)
		}
	}
}

func TestFinishReviewPageStatesTheSettings(t *testing.T) {
	doc := docparity.Read(t, "docs/finish-review.md")
	for _, want := range []string{
		"| `finish_review.enabled` | `true` | boolean |",
		"| `finish_review.max_rounds` | 2 | 0 to 5 |",
		"| `finish_review.scratch_patterns` | none | up to 32 |",
		"cap of 32",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/finish-review.md does not state %q", want)
		}
	}
	if DefaultFinishReviewMaxRounds != 2 || maxFinishReviewRounds != 5 || MaxFinishReviewPatterns != 32 {
		t.Error("the constants disagree with the page")
	}
}
