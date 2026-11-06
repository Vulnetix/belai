package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

func TestHandoffDefaults(t *testing.T) {
	for _, s := range []Settings{{}, {Handoff: &HandoffSettings{}}} {
		if !s.HandoffEnabled() || !s.HandoffIncludeGoal() || s.HandoffMaxBytes() != 16384 {
			t.Fatalf("defaults wrong for %+v", s.Handoff)
		}
	}
}

func TestHandoffMaxBytesIsClamped(t *testing.T) {
	for in, want := range map[int]int{-1: 2048, 0: 2048, 2047: 2048, 2048: 2048, 30000: 30000, 65536: 65536, 70000: 65536} {
		s := Settings{Handoff: &HandoffSettings{MaxBytes: ip(in)}}
		if got := s.HandoffMaxBytes(); got != want {
			t.Errorf("max_bytes %d -> %d, want %d", in, got, want)
		}
		if err := ValidateSettings(s); err != nil {
			t.Errorf("max_bytes %d failed validation: %v", in, err)
		}
	}
}

func TestHandoffUserLayerSetsAnything(t *testing.T) {
	got := mergeHandoff(&HandoffSettings{Enabled: bp(false), IncludeGoal: bp(false), MaxBytes: ip(2048)},
		&HandoffSettings{Enabled: bp(true), IncludeGoal: bp(true), MaxBytes: ip(65536)}, false)
	s := Settings{Handoff: got}
	if !s.HandoffEnabled() || !s.HandoffIncludeGoal() || s.HandoffMaxBytes() != 65536 {
		t.Fatalf("user layer not honoured: %+v", got)
	}
}

func TestHandoffProjectCanOnlyNarrow(t *testing.T) {
	user := &HandoffSettings{Enabled: bp(false), IncludeGoal: bp(false), MaxBytes: ip(4096)}
	got := mergeHandoff(user, &HandoffSettings{Enabled: bp(true), IncludeGoal: bp(true), MaxBytes: ip(60000)}, true)
	s := Settings{Handoff: got}
	if s.HandoffEnabled() || s.HandoffIncludeGoal() || s.HandoffMaxBytes() != 4096 {
		t.Fatalf("a project widened: %+v", got)
	}
	// With the user's defaults a project can still not set a switch true.
	got = mergeHandoff(nil, &HandoffSettings{Enabled: bp(true), IncludeGoal: bp(true), MaxBytes: ip(16384)}, true)
	if got.Enabled != nil || got.IncludeGoal != nil || got.MaxBytes != nil {
		t.Fatalf("a no-op or widening project value was stored: %+v", got)
	}
	// Turning off and lowering apply; a value below the floor is clamped to it.
	got = mergeHandoff(nil, &HandoffSettings{Enabled: bp(false), IncludeGoal: bp(false), MaxBytes: ip(10)}, true)
	s = Settings{Handoff: got}
	if s.HandoffEnabled() || s.HandoffIncludeGoal() || s.HandoffMaxBytes() != 2048 {
		t.Fatalf("a project could not narrow: %+v", got)
	}
}

func TestHandoffOverrideNarrowsOnly(t *testing.T) {
	user := Settings{Handoff: &HandoffSettings{MaxBytes: ip(30000)}}
	out := user.Override(Settings{Handoff: &HandoffSettings{MaxBytes: ip(40000), IncludeGoal: bp(false), Enabled: bp(true)}})
	if out.HandoffMaxBytes() != 30000 || out.HandoffIncludeGoal() || !out.HandoffEnabled() {
		t.Fatalf("Override: %+v", out.Handoff)
	}
	out = user.Override(Settings{Handoff: &HandoffSettings{MaxBytes: ip(8000)}})
	if out.HandoffMaxBytes() != 8000 {
		t.Fatalf("Override did not lower: %+v", out.Handoff)
	}
}

func TestHandoffJSONRoundTrip(t *testing.T) {
	var s Settings
	if err := json.Unmarshal([]byte(`{"handoff":{"enabled":false,"max_bytes":4096,"include_goal":false}}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.HandoffEnabled() || s.HandoffIncludeGoal() || s.HandoffMaxBytes() != 4096 {
		t.Fatalf("%+v", s.Handoff)
	}
}

func TestResolveHandoffProjectLayer(t *testing.T) {
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
	if err := SaveGlobal(Settings{Handoff: &HandoffSettings{Enabled: bp(false), IncludeGoal: bp(false), MaxBytes: ip(4096)}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveProject(workdir, Settings{Handoff: &HandoffSettings{Enabled: bp(true), IncludeGoal: bp(true), MaxBytes: ip(60000)}}); err != nil {
		t.Fatal(err)
	}
	eff := resolve()
	if eff.Settings.HandoffEnabled() || eff.Settings.HandoffIncludeGoal() || eff.Settings.HandoffMaxBytes() != 4096 {
		t.Fatalf("project widened: %+v", eff.Settings.Handoff)
	}
	noted := false
	for _, n := range eff.Notes {
		if strings.Contains(n, "handoff.enabled") && strings.Contains(n, "handoff.include_goal") && strings.Contains(n, "handoff.max_bytes") {
			noted = true
		}
	}
	if !noted {
		t.Fatalf("no note for the ignored project keys: %v", eff.Notes)
	}
	// A user who left the defaults: a project lowers the cap and drops the goal facts.
	if err := SaveGlobal(Settings{}); err != nil {
		t.Fatal(err)
	}
	if err := SaveProject(workdir, Settings{Handoff: &HandoffSettings{IncludeGoal: bp(false), MaxBytes: ip(3000)}}); err != nil {
		t.Fatal(err)
	}
	eff = resolve()
	if eff.Settings.HandoffIncludeGoal() || eff.Settings.HandoffMaxBytes() != 3000 || eff.Origin["handoff"] != SourceProject {
		t.Fatalf("project narrowing not applied: %+v origin %v", eff.Settings.Handoff, eff.Origin["handoff"])
	}
	// A project cannot switch /relay on over a user's off.
	if err := SaveGlobal(Settings{Handoff: &HandoffSettings{Enabled: bp(false)}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveProject(workdir, Settings{Handoff: &HandoffSettings{Enabled: bp(true)}}); err != nil {
		t.Fatal(err)
	}
	if resolve().Settings.HandoffEnabled() {
		t.Fatal("a project turned /relay on")
	}
}

// TestHandoffPageStatesEachDefaultAndRange keeps docs/handoff.md equal to the
// bounds the code clamps to.
func TestHandoffPageStatesEachDefaultAndRange(t *testing.T) {
	doc := docparity.Read(t, "docs/handoff.md")
	for _, want := range []string{
		"| `handoff.enabled` | on |",
		"| `handoff.include_goal` | on |",
		fmt.Sprintf("| `handoff.max_bytes` | %d | %d to %d |", DefaultHandoffMaxBytes, MinHandoffMaxBytes, MaxHandoffMaxBytes),
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/handoff.md does not state %q", want)
		}
	}
}
