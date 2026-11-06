package config

import (
	"encoding/json"
	"math"
	"path/filepath"
	"strings"
	"testing"
)

func fp(v float64) *float64 { return &v }

// R1: every cap is off and the warning share and extension count have their
// defaults when nothing is set.
func TestRunCapsRule1_Defaults(t *testing.T) {
	for _, s := range []Settings{{}, {RunCaps: &RunCapsSettings{}}} {
		if s.RunCapsActive() || s.RunCapCostMicroUSD() != 0 || s.RunCapTokens() != 0 || s.RunCapWallMinutes() != 0 ||
			s.RunCapFinalizeAt() != 0.9 || s.RunCapMaxExtends() != 3 {
			t.Fatalf("defaults wrong for %+v", s.RunCaps)
		}
	}
	s := Settings{RunCaps: &RunCapsSettings{MaxCostUSD: fp(1.5), MaxTokens: ip(2000), MaxWallMinutes: ip(30), FinalizeAt: fp(0.8), MaxExtends: ip(0)}}
	if s.RunCapCostMicroUSD() != 1_500_000 || s.RunCapTokens() != 2000 || s.RunCapWallMinutes() != 30 ||
		s.RunCapFinalizeAt() != 0.8 || s.RunCapMaxExtends() != 0 || !s.RunCapsActive() {
		t.Fatalf("values not honoured: %+v", s.RunCaps)
	}
	var parsed Settings
	if err := json.Unmarshal([]byte(`{"run_caps":{"max_cost_usd":2,"max_tokens":9,"max_wall_minutes":5,"finalize_at":0.5,"max_extends":1}}`), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.RunCapCostMicroUSD() != 2_000_000 || parsed.RunCapMaxExtends() != 1 {
		t.Fatalf("parsed %+v", parsed.RunCaps)
	}
}

// R10: a project layer adds or lowers a cap and never raises or clears one; a
// user layer sets any value.
func TestRunCapsRule10_ProjectOnlyNarrows(t *testing.T) {
	global := Settings{RunCaps: &RunCapsSettings{MaxCostUSD: fp(5), MaxTokens: ip(1000), FinalizeAt: fp(0.9), MaxExtends: ip(3)}}
	loose := Settings{RunCaps: &RunCapsSettings{MaxCostUSD: fp(50), MaxTokens: ip(0), MaxWallMinutes: ip(0), FinalizeAt: fp(0.95), MaxExtends: ip(9)}}
	got := global.Override(loose)
	if got.RunCapCostMicroUSD() != 5_000_000 || got.RunCapTokens() != 1000 || got.RunCapWallMinutes() != 0 ||
		got.RunCapFinalizeAt() != 0.9 || got.RunCapMaxExtends() != 3 {
		t.Fatalf("a project layer loosened a cap: %+v", got.RunCaps)
	}
	tight := Settings{RunCaps: &RunCapsSettings{MaxCostUSD: fp(2), MaxTokens: ip(500), MaxWallMinutes: ip(10), FinalizeAt: fp(0.7), MaxExtends: ip(0)}}
	got = global.Override(tight)
	if got.RunCapCostMicroUSD() != 2_000_000 || got.RunCapTokens() != 500 || got.RunCapWallMinutes() != 10 ||
		got.RunCapFinalizeAt() != 0.7 || got.RunCapMaxExtends() != 0 {
		t.Fatalf("a project layer could not narrow: %+v", got.RunCaps)
	}
	// A project may add a cap where the user set none.
	if got := (Settings{}).Override(tight); got.RunCapTokens() != 500 {
		t.Fatalf("project cap not added: %+v", got.RunCaps)
	}
	// Out of range and malformed values from a project are dropped, not kept.
	bad := Settings{RunCaps: &RunCapsSettings{MaxCostUSD: fp(math.NaN()), MaxTokens: ip(-3), FinalizeAt: fp(1.5), MaxExtends: ip(-1)}}
	got = (Settings{}).Override(bad)
	if got.RunCaps != nil && (got.RunCaps.MaxCostUSD != nil || got.RunCaps.MaxTokens != nil || got.RunCaps.FinalizeAt != nil || got.RunCaps.MaxExtends != nil) {
		t.Fatalf("a malformed project value was kept: %+v", got.RunCaps)
	}
	if err := ValidateRunCaps(got); err != nil {
		t.Fatalf("the merged result fails validation: %v", err)
	}
	// A user layer sets freely, a zero included.
	user := mergeRunCaps(global.RunCaps, &RunCapsSettings{MaxCostUSD: fp(0), MaxTokens: ip(9999), MaxExtends: ip(10)}, false)
	merged := Settings{RunCaps: user}
	if merged.RunCapCostMicroUSD() != 0 || merged.RunCapTokens() != 9999 || merged.RunCapMaxExtends() != 10 {
		t.Fatalf("the user layer could not raise: %+v", user)
	}
}

func TestRunCapsRule10_ResolveLayersAndNotes(t *testing.T) {
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
	if err := SaveGlobal(Settings{RunCaps: &RunCapsSettings{MaxTokens: ip(1000)}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveProject(workdir, Settings{RunCaps: &RunCapsSettings{MaxTokens: ip(5000), FinalizeAt: fp(2)}}); err != nil {
		t.Fatal(err)
	}
	eff := resolve()
	if eff.Settings.RunCapTokens() != 1000 || eff.Settings.RunCapFinalizeAt() != 0.9 {
		t.Fatalf("project raised: %+v", eff.Settings.RunCaps)
	}
	if eff.Origin["run_caps"] != SourceGlobal {
		t.Fatalf("origin %v", eff.Origin["run_caps"])
	}
	noted := false
	for _, n := range eff.Notes {
		if strings.Contains(n, "run_caps.max_tokens") && strings.Contains(n, "run_caps.finalize_at") {
			noted = true
		}
	}
	if !noted {
		t.Fatalf("no note for the ignored project keys: %v", eff.Notes)
	}
	if err := SaveProject(workdir, Settings{RunCaps: &RunCapsSettings{MaxTokens: ip(400)}}); err != nil {
		t.Fatal(err)
	}
	if eff := resolve(); eff.Settings.RunCapTokens() != 400 || eff.Origin["run_caps"] != SourceProject {
		t.Fatalf("project narrowing not applied: %d %v", eff.Settings.RunCapTokens(), eff.Origin["run_caps"])
	}
	// A flag layer is the user's own and raises.
	eff, err := Resolve(workdir, func(string) string { return "" }, Settings{RunCaps: &RunCapsSettings{MaxTokens: ip(9000)}})
	if err != nil || eff.Settings.RunCapTokens() != 9000 {
		t.Fatalf("a flag could not raise: %v %d", err, eff.Settings.RunCapTokens())
	}
	// A malformed user value refuses the load.
	if err := SaveProject(workdir, Settings{}); err != nil {
		t.Fatal(err)
	}
	if err := SaveGlobal(Settings{RunCaps: &RunCapsSettings{MaxTokens: ip(-1)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(workdir, func(string) string { return "" }, Settings{}); err == nil {
		t.Fatal("a negative user cap was accepted")
	}
}

// R11: a malformed cap fails validation, naming its key.
func TestRunCapsRule11_ValidationNamesTheKey(t *testing.T) {
	cases := []struct {
		rc   RunCapsSettings
		want string
	}{
		{RunCapsSettings{MaxCostUSD: fp(-1)}, "run_caps.max_cost_usd"},
		{RunCapsSettings{MaxCostUSD: fp(math.NaN())}, "run_caps.max_cost_usd"},
		{RunCapsSettings{MaxCostUSD: fp(1e9)}, "run_caps.max_cost_usd"},
		{RunCapsSettings{MaxTokens: ip(-1)}, "run_caps.max_tokens"},
		{RunCapsSettings{MaxWallMinutes: ip(-1)}, "run_caps.max_wall_minutes"},
		{RunCapsSettings{MaxWallMinutes: ip(43201)}, "run_caps.max_wall_minutes"},
		{RunCapsSettings{FinalizeAt: fp(0)}, "run_caps.finalize_at"},
		{RunCapsSettings{FinalizeAt: fp(1)}, "run_caps.finalize_at"},
		{RunCapsSettings{MaxExtends: ip(21)}, "run_caps.max_extends"},
	}
	for _, c := range cases {
		c := c
		s := Settings{RunCaps: &c.rc}
		err := ValidateRunCaps(s)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: err = %v, want it to name %s", c.rc, err, c.want)
		}
		if ValidateSettings(s) == nil {
			t.Errorf("%+v: ValidateSettings accepted it", c.rc)
		}
		// An accessor never returns a malformed value, so a caller that skipped
		// validation still gets a cap that is off or a default.
		if s.RunCapCostMicroUSD() < 0 || s.RunCapTokens() < 0 || s.RunCapWallMinutes() < 0 ||
			s.RunCapFinalizeAt() <= 0 || s.RunCapFinalizeAt() >= 1 || s.RunCapMaxExtends() < 0 {
			t.Errorf("%+v: an accessor returned a malformed value", c.rc)
		}
	}
	if err := ValidateRunCaps(Settings{}); err != nil {
		t.Fatal(err)
	}
}

// R12: the state file of a session lives inside the run-caps directory under a
// plain or hashed name.
func TestRunCapsRule12_StatePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BELAI_HOME", home)
	dir, err := RunCapsDir()
	if err != nil || dir != filepath.Join(home, "run_caps") {
		t.Fatalf("dir %q %v", dir, err)
	}
	plain, err := RunCapsFile("abc-123_x.y")
	if err != nil || plain != filepath.Join(dir, "abc-123_x.y.json") {
		t.Fatalf("plain %q %v", plain, err)
	}
	for _, id := range []string{"../x", "a/b", ".hidden", strings.Repeat("a", 65), "sp ace", "ü"} {
		p, err := RunCapsFile(id)
		if err != nil || filepath.Dir(p) != dir || len(filepath.Base(p)) != 64+len(".json") {
			t.Errorf("%q gave %q %v", id, p, err)
		}
	}
	if _, err := RunCapsFile(""); err == nil {
		t.Fatal("an empty id was accepted")
	}
}
