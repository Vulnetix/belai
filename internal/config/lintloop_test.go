package config

import (
	"strings"
	"testing"
)

func TestLintLoopDefaultsOffWithBoundedNumbers(t *testing.T) {
	var s Settings
	if s.LintLoopEnabled() {
		t.Fatal("the lint loop must default off")
	}
	if s.LintLoopTimeoutMS() != 10_000 || s.LintLoopMaxAttempts() != 3 || s.LintLoopMaxRows() != 10 {
		t.Fatalf("defaults = %d %d %d", s.LintLoopTimeoutMS(), s.LintLoopMaxAttempts(), s.LintLoopMaxRows())
	}
	no := false
	if (Settings{LintLoop: &LintLoopSettings{Enabled: &no}}).LintLoopEnabled() {
		t.Fatal("false must be off")
	}
	// An out-of-range value in a loaded struct falls back to the default.
	bad := Settings{LintLoop: &LintLoopSettings{TimeoutMS: ip(5), MaxAttempts: ip(99), MaxRows: ip(0)}}
	if bad.LintLoopTimeoutMS() != 10_000 || bad.LintLoopMaxAttempts() != 3 || bad.LintLoopMaxRows() != 10 {
		t.Fatal("an out-of-range number reached an accessor")
	}
}

func TestValidateLintLoopNamesTheKey(t *testing.T) {
	for key, l := range map[string]LintLoopSettings{
		"lint_loop.timeout_ms":   {TimeoutMS: ip(999)},
		"lint_loop.max_attempts": {MaxAttempts: ip(7)},
		"lint_loop.max_rows":     {MaxRows: ip(21)},
	} {
		l := l
		err := ValidateLintLoop(Settings{LintLoop: &l})
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("%s: err = %v", key, err)
		}
	}
	ok := LintLoopSettings{TimeoutMS: ip(60_000), MaxAttempts: ip(1), MaxRows: ip(20)}
	if err := ValidateLintLoop(Settings{LintLoop: &ok}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateLintLoop(Settings{}); err != nil {
		t.Fatal(err)
	}
}

func TestLintLoopProjectLayerCanOnlyNarrow(t *testing.T) {
	yes, no := true, false
	user := Settings{LintLoop: &LintLoopSettings{Enabled: &yes, TimeoutMS: ip(20_000), MaxRows: ip(15)}}

	// A project cannot switch it on.
	if got := (Settings{}).Override(Settings{LintLoop: &LintLoopSettings{Enabled: &yes}}); got.LintLoopEnabled() {
		t.Fatal("a project layer turned the lint loop on")
	}
	if got := (Settings{LintLoop: &LintLoopSettings{Enabled: &no}}).Override(Settings{LintLoop: &LintLoopSettings{Enabled: &yes}}); got.LintLoopEnabled() {
		t.Fatal("a project layer turned back on what the user turned off")
	}
	// It can switch it off.
	if got := user.Override(Settings{LintLoop: &LintLoopSettings{Enabled: &no}}); got.LintLoopEnabled() {
		t.Fatal("a project layer could not turn the lint loop off")
	}
	// It lowers numbers and never raises them.
	got := user.Override(Settings{LintLoop: &LintLoopSettings{TimeoutMS: ip(5_000), MaxRows: ip(30), MaxAttempts: ip(5)}})
	if got.LintLoopTimeoutMS() != 5_000 {
		t.Fatalf("timeout = %d, want the lowered 5000", got.LintLoopTimeoutMS())
	}
	if got.LintLoopMaxRows() != 15 {
		t.Fatalf("max_rows = %d: a project raised it (or set it out of range)", got.LintLoopMaxRows())
	}
	if got.LintLoopMaxAttempts() != 3 {
		t.Fatalf("max_attempts = %d: a project raised it above the default", got.LintLoopMaxAttempts())
	}
	// An equal value and an out-of-range value from a project are dropped.
	if g := (Settings{}).Override(Settings{LintLoop: &LintLoopSettings{TimeoutMS: ip(10_000)}}); g.LintLoop != nil && g.LintLoop.TimeoutMS != nil {
		t.Fatal("a project value equal to the default was applied")
	}
	if g := (Settings{}).Override(Settings{LintLoop: &LintLoopSettings{TimeoutMS: ip(1)}}); g.LintLoopTimeoutMS() != 10_000 {
		t.Fatal("an out-of-range project value was applied")
	}
}

func TestLintLoopUserLayersSetAnyKeyInRange(t *testing.T) {
	yes := true
	eff := Effective{Origin: map[string]Source{}}
	eff.apply(Settings{LintLoop: &LintLoopSettings{Enabled: &yes, TimeoutMS: ip(60_000), MaxRows: ip(20), MaxAttempts: ip(6)}}, SourceGlobal)
	s := eff.Settings
	if !s.LintLoopEnabled() || s.LintLoopTimeoutMS() != 60_000 || s.LintLoopMaxRows() != 20 || s.LintLoopMaxAttempts() != 6 {
		t.Fatalf("user layer lost: %+v", s.LintLoop)
	}
	if eff.Origin["lint_loop"] != SourceGlobal {
		t.Fatalf("origin = %q", eff.Origin["lint_loop"])
	}
	// A later project layer cannot raise or switch on; a later user layer that
	// does not name a key leaves it alone.
	eff.apply(Settings{LintLoop: &LintLoopSettings{MaxRows: ip(25)}}, SourceProject)
	if eff.Settings.LintLoopMaxRows() != 20 {
		t.Fatal("project raised max_rows")
	}
	eff.apply(Settings{LintLoop: &LintLoopSettings{MaxRows: ip(5)}}, SourceProjectPrefs)
	if eff.Settings.LintLoopMaxRows() != 5 || !eff.Settings.LintLoopEnabled() {
		t.Fatalf("prefs layer: %+v", eff.Settings.LintLoop)
	}
}

func TestResolveDropsProjectLintLoopEnableWithANote(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()
	yes := true
	if err := SaveProject(workdir, Settings{LintLoop: &LintLoopSettings{Enabled: &yes, MaxRows: ip(18)}}); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}
	eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if eff.Settings.LintLoopEnabled() {
		t.Fatal("a project turned the lint loop on")
	}
	if !hasNote(eff.Notes, "project lint_loop.enabled, lint_loop.max_rows ignored") {
		t.Fatalf("no drop note: %v", eff.Notes)
	}
}

func TestLintLoopEnvIsTheUsersOwn(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	for v, want := range map[string]bool{"1": true, "true": true, "0": false, "false": false} {
		eff, err := Resolve(t.TempDir(), func(k string) string {
			if k == "BELAI_LINT_LOOP" {
				return v
			}
			return ""
		}, Settings{})
		if err != nil {
			t.Fatal(err)
		}
		if eff.Settings.LintLoopEnabled() != want {
			t.Errorf("BELAI_LINT_LOOP=%s: enabled = %v", v, eff.Settings.LintLoopEnabled())
		}
	}
	if lintLoopEnv("maybe") != nil || lintLoopEnv("") != nil {
		t.Fatal("a non-boolean must be no layer")
	}
}

func TestLintLoopProjectPrefsMayTurnItOn(t *testing.T) {
	var p ProjectPrefs
	if err := p.SetFlat("lint_loop.enabled", true); err != nil {
		t.Fatal(err)
	}
	if !p.toSettings().LintLoopEnabled() || p.Flat()["lint_loop.enabled"] != true {
		t.Fatalf("pref lost: %+v", p)
	}
	if err := p.SetFlat("lint_loop.enabled", "yes"); err == nil {
		t.Fatal("a non-boolean pref was accepted")
	}
	if err := p.SetFlat("lint_loop.timeout_ms", 5); err == nil {
		t.Fatal("a number key is not a pref")
	}
	if err := p.UnsetFlat("lint_loop.enabled"); err != nil || p.LintLoop != nil {
		t.Fatalf("unset left %+v (%v)", p.LintLoop, err)
	}
}

func TestProjectLintLoopPathIsUnderTheBelaiDir(t *testing.T) {
	if got := ProjectLintLoopPath("/r"); got != "/r/.vulnetix/belai/lint_loop.json" {
		t.Fatalf("path = %q", got)
	}
}
