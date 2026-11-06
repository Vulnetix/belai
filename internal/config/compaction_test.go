package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

func TestCompactionDefaults(t *testing.T) {
	for _, s := range []Settings{{}, {Compaction: &CompactionSettings{}}} {
		if s.CompactThresholdPct() != 70 || s.CompactKeepRecentTurns() != 6 || s.CompactKeepResultChars() != 300 ||
			s.CompactKeepToolResults() != 2 || s.CompactBreakerFailures() != 3 || s.CompactBreakerCooldownPasses() != 4 {
			t.Fatalf("defaults wrong for %+v", s.Compaction)
		}
	}
}

func TestCompactionValuesAreUsed(t *testing.T) {
	s := Settings{Compaction: &CompactionSettings{
		ThresholdPct: ip(50), KeepRecentTurns: ip(10), KeepResultChars: ip(0), KeepToolResults: ip(0),
		BreakerFailures: ip(1), BreakerCooldownPasses: ip(20),
	}}
	if s.CompactThresholdPct() != 50 || s.CompactKeepRecentTurns() != 10 || s.CompactKeepResultChars() != 0 ||
		s.CompactKeepToolResults() != 0 || s.CompactBreakerFailures() != 1 || s.CompactBreakerCooldownPasses() != 20 {
		t.Fatalf("values not honoured: %+v", s.Compaction)
	}
}

func TestCompactionOutOfRangeClampsToDefaultAndFailsValidation(t *testing.T) {
	bad := []struct {
		name string
		c    CompactionSettings
		want string
	}{
		{"threshold low", CompactionSettings{ThresholdPct: ip(29)}, "compaction.threshold_pct"},
		{"threshold high", CompactionSettings{ThresholdPct: ip(96)}, "compaction.threshold_pct"},
		{"recent low", CompactionSettings{KeepRecentTurns: ip(1)}, "compaction.keep_recent_turns"},
		{"recent high", CompactionSettings{KeepRecentTurns: ip(41)}, "compaction.keep_recent_turns"},
		{"chars neg", CompactionSettings{KeepResultChars: ip(-1)}, "compaction.keep_result_chars"},
		{"chars high", CompactionSettings{KeepResultChars: ip(2001)}, "compaction.keep_result_chars"},
		{"results neg", CompactionSettings{KeepToolResults: ip(-1)}, "compaction.keep_tool_results"},
		{"failures zero", CompactionSettings{BreakerFailures: ip(0)}, "compaction.breaker_failures"},
		{"cooldown high", CompactionSettings{BreakerCooldownPasses: ip(21)}, "compaction.breaker_cooldown_passes"},
	}
	for _, tc := range bad {
		s := Settings{Compaction: &tc.c}
		err := ValidateCompaction(s)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to name %s", tc.name, err, tc.want)
		}
		if err := ValidateSettings(s); err == nil {
			t.Errorf("%s: ValidateSettings accepted it", tc.name)
		}
		d := Settings{}
		if s.CompactThresholdPct() != d.CompactThresholdPct() || s.CompactKeepRecentTurns() != d.CompactKeepRecentTurns() ||
			s.CompactKeepResultChars() != d.CompactKeepResultChars() || s.CompactKeepToolResults() != d.CompactKeepToolResults() ||
			s.CompactBreakerFailures() != d.CompactBreakerFailures() || s.CompactBreakerCooldownPasses() != d.CompactBreakerCooldownPasses() {
			t.Errorf("%s: an accessor returned an out-of-range value", tc.name)
		}
	}
	if err := ValidateCompaction(Settings{}); err != nil {
		t.Fatal(err)
	}
}

func TestCompactionParse(t *testing.T) {
	var s Settings
	if err := json.Unmarshal([]byte(`{"compaction":{"threshold_pct":55,"keep_result_chars":0}}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.CompactThresholdPct() != 55 || s.CompactKeepResultChars() != 0 {
		t.Fatalf("parsed %+v", s.Compaction)
	}
}

func TestMergeCompactionProjectOnlyTightens(t *testing.T) {
	user := &CompactionSettings{ThresholdPct: ip(80), KeepRecentTurns: ip(10), BreakerFailures: ip(5), BreakerCooldownPasses: ip(10)}
	// Each of these moves the wrong way and must be dropped.
	loosen := &CompactionSettings{
		ThresholdPct: ip(90), KeepRecentTurns: ip(4), KeepResultChars: ip(100), KeepToolResults: ip(1),
		BreakerFailures: ip(2), BreakerCooldownPasses: ip(15),
	}
	got := mergeCompaction(user, loosen, true)
	if *got.ThresholdPct != 80 || *got.KeepRecentTurns != 10 || *got.BreakerFailures != 5 || *got.BreakerCooldownPasses != 10 {
		t.Fatalf("a project loosened the user's values: %+v", got)
	}
	if got.KeepResultChars != nil || got.KeepToolResults != nil {
		t.Fatalf("a project lowered a keep count below the default: %+v", got)
	}
	// Each of these tightens and applies.
	tighten := &CompactionSettings{
		ThresholdPct: ip(60), KeepRecentTurns: ip(12), KeepResultChars: ip(500), KeepToolResults: ip(4),
		BreakerFailures: ip(6), BreakerCooldownPasses: ip(2),
	}
	got = mergeCompaction(user, tighten, true)
	if *got.ThresholdPct != 60 || *got.KeepRecentTurns != 12 || *got.KeepResultChars != 500 || *got.KeepToolResults != 4 ||
		*got.BreakerFailures != 6 || *got.BreakerCooldownPasses != 2 {
		t.Fatalf("a tightening project value was not applied: %+v", got)
	}
	// The input is not mutated.
	if *user.ThresholdPct != 80 {
		t.Fatal("mergeCompaction mutated its input")
	}
}

func TestMergeCompactionProjectIsComparedWithTheDefault(t *testing.T) {
	// Nothing set by the user: the default is the bar, so 90 (looser than 70) is dropped.
	got := mergeCompaction(nil, &CompactionSettings{ThresholdPct: ip(90), KeepRecentTurns: ip(3)}, true)
	if got.ThresholdPct != nil || got.KeepRecentTurns != nil {
		t.Fatalf("a project value looser than the default applied: %+v", got)
	}
	got = mergeCompaction(nil, &CompactionSettings{ThresholdPct: ip(40)}, true)
	if got.ThresholdPct == nil || *got.ThresholdPct != 40 {
		t.Fatal("a project could not compact sooner than the default")
	}
}

func TestMergeCompactionProjectOutOfRangeIsDropped(t *testing.T) {
	// 5 is lower (tighter) than any default but outside the bounds.
	got := mergeCompaction(nil, &CompactionSettings{ThresholdPct: ip(5), BreakerCooldownPasses: ip(0), KeepRecentTurns: ip(999)}, true)
	if got.ThresholdPct != nil || got.BreakerCooldownPasses != nil || got.KeepRecentTurns != nil {
		t.Fatalf("out-of-range project values applied: %+v", got)
	}
	if err := ValidateCompaction(Settings{Compaction: got}); err != nil {
		t.Fatalf("a project file made the merged settings invalid: %v", err)
	}
}

func TestMergeCompactionUserSetsFreely(t *testing.T) {
	user := &CompactionSettings{ThresholdPct: ip(50)}
	got := mergeCompaction(user, &CompactionSettings{ThresholdPct: ip(90), KeepRecentTurns: ip(2)}, false)
	if *got.ThresholdPct != 90 || *got.KeepRecentTurns != 2 {
		t.Fatalf("a user layer could not loosen: %+v", got)
	}
	if mergeCompaction(user, nil, true) != user {
		t.Fatal("a nil layer changed the settings")
	}
}

func TestOverrideProjectCannotLoosenCompaction(t *testing.T) {
	global := Settings{Compaction: &CompactionSettings{ThresholdPct: ip(60)}}
	if got := global.Override(Settings{Compaction: &CompactionSettings{ThresholdPct: ip(95)}}); got.CompactThresholdPct() != 60 {
		t.Fatalf("a project layer raised the threshold to %d", got.CompactThresholdPct())
	}
	if got := (Settings{}).Override(Settings{Compaction: &CompactionSettings{ThresholdPct: ip(40)}}); got.CompactThresholdPct() != 40 {
		t.Fatalf("a project layer could not lower the threshold, got %d", got.CompactThresholdPct())
	}
}

func TestResolveCompactionProjectLayer(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()
	resolve := func() Effective {
		t.Helper()
		eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
		if err != nil {
			t.Fatal(err)
		}
		return eff
	}
	if err := SaveGlobal(Settings{Compaction: &CompactionSettings{ThresholdPct: ip(60)}}); err != nil {
		t.Fatal(err)
	}
	// Looser and out of range: ignored, noted, and Resolve still succeeds.
	if err := SaveProject(workdir, Settings{Compaction: &CompactionSettings{ThresholdPct: ip(90), KeepRecentTurns: ip(1)}}); err != nil {
		t.Fatal(err)
	}
	eff := resolve()
	if eff.Settings.CompactThresholdPct() != 60 || eff.Settings.CompactKeepRecentTurns() != 6 {
		t.Fatalf("project loosened: %+v", eff.Settings.Compaction)
	}
	if eff.Origin["compaction"] != SourceGlobal {
		t.Fatalf("origin = %v", eff.Origin["compaction"])
	}
	noted := false
	for _, n := range eff.Notes {
		if strings.Contains(n, "compaction.threshold_pct") && strings.Contains(n, "compaction.keep_recent_turns") {
			noted = true
		}
	}
	if !noted {
		t.Fatalf("no note for the ignored project keys: %v", eff.Notes)
	}
	// Tighter: applied.
	if err := SaveProject(workdir, Settings{Compaction: &CompactionSettings{ThresholdPct: ip(45)}}); err != nil {
		t.Fatal(err)
	}
	eff = resolve()
	if eff.Settings.CompactThresholdPct() != 45 || eff.Origin["compaction"] != SourceProject {
		t.Fatalf("project tightening not applied: %d %v", eff.Settings.CompactThresholdPct(), eff.Origin["compaction"])
	}
	// A user layer with an out-of-range value fails the load.
	if err := SaveProject(workdir, Settings{}); err != nil {
		t.Fatal(err)
	}
	if err := SaveGlobal(Settings{Compaction: &CompactionSettings{ThresholdPct: ip(5)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(workdir, func(string) string { return "" }, Settings{}); err == nil {
		t.Fatal("an out-of-range user value was accepted")
	}
}

// TestCompactionPageStatesEachDefaultAndRange keeps the settings table in
// docs/compaction.md equal to the table the code validates against.
func TestCompactionPageStatesEachDefaultAndRange(t *testing.T) {
	doc := docparity.Read(t, "docs/compaction.md")
	for _, f := range compactionFields {
		row := fmt.Sprintf("| `%s` | %d | %d to %d |", f.key, f.def, f.min, f.max)
		if !strings.Contains(doc, row) {
			t.Errorf("docs/compaction.md does not state %q", row)
		}
		dir := "higher"
		if f.lowerIsTighter {
			dir = "lower"
		}
		short := strings.TrimPrefix(f.key, "compaction.")
		want := fmt.Sprintf("| `%s` | %s than the value in force |", short, dir)
		if !strings.Contains(doc, want) {
			t.Errorf("docs/compaction.md does not state %q", want)
		}
	}
}
