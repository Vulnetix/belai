package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func i64p(v int64) *int64 { return &v }

func TestCheckpointsDefaultOnWithBoundedNumbers(t *testing.T) {
	var s Settings
	if !s.CheckpointsEnabled() {
		t.Fatal("checkpoints must default on")
	}
	if s.CheckpointsMaxPerSession() != 50 || s.CheckpointsMaxBytes() != 200<<20 || s.CheckpointsRetainDays() != 7 {
		t.Fatalf("defaults = %d %d %d", s.CheckpointsMaxPerSession(), s.CheckpointsMaxBytes(), s.CheckpointsRetainDays())
	}
	no := false
	if (Settings{Checkpoints: &CheckpointSettings{Enabled: &no}}).CheckpointsEnabled() {
		t.Fatal("false must be off")
	}
	bad := Settings{Checkpoints: &CheckpointSettings{MaxPerSession: ip(0), MaxBytes: i64p(5), RetainDays: ip(9999)}}
	if bad.CheckpointsMaxPerSession() != 50 || bad.CheckpointsMaxBytes() != 200<<20 || bad.CheckpointsRetainDays() != 7 {
		t.Fatal("an out-of-range number reached an accessor")
	}
}

func TestValidateCheckpointsNamesTheKey(t *testing.T) {
	for key, c := range map[string]CheckpointSettings{
		"checkpoints.max_per_session": {MaxPerSession: ip(501)},
		"checkpoints.max_bytes":       {MaxBytes: i64p(1)},
		"checkpoints.retain_days":     {RetainDays: ip(0)},
	} {
		c := c
		err := ValidateCheckpoints(Settings{Checkpoints: &c})
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("%s: err = %v", key, err)
		}
	}
	ok := CheckpointSettings{MaxPerSession: ip(500), MaxBytes: i64p(10 << 30), RetainDays: ip(365)}
	if err := ValidateCheckpoints(Settings{Checkpoints: &ok}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCheckpoints(Settings{}); err != nil {
		t.Fatal(err)
	}
}

// A repository may switch checkpoints off and do nothing else: a lower limit or
// a shorter retention would delete the history the user already has.
func TestCheckpointsProjectLayerCanOnlySwitchOff(t *testing.T) {
	yes, no := true, false
	user := Settings{Checkpoints: &CheckpointSettings{MaxPerSession: ip(80), MaxBytes: i64p(500 << 20), RetainDays: ip(30)}}

	got := user.Override(Settings{Checkpoints: &CheckpointSettings{Enabled: &no}})
	if got.CheckpointsEnabled() {
		t.Fatal("a project layer could not switch checkpoints off")
	}
	if got.CheckpointsMaxPerSession() != 80 || got.CheckpointsRetainDays() != 30 {
		t.Fatal("switching off changed the user's limits")
	}
	// It cannot turn them back on after the user turned them off.
	off := Settings{Checkpoints: &CheckpointSettings{Enabled: &no}}
	if off.Override(Settings{Checkpoints: &CheckpointSettings{Enabled: &yes}}).CheckpointsEnabled() {
		t.Fatal("a project layer turned back on what the user turned off")
	}
	// It cannot change a single limit, lower or higher.
	got = user.Override(Settings{Checkpoints: &CheckpointSettings{MaxPerSession: ip(1), MaxBytes: i64p(2 << 20), RetainDays: ip(1)}})
	if got.CheckpointsMaxPerSession() != 80 || got.CheckpointsMaxBytes() != 500<<20 || got.CheckpointsRetainDays() != 30 {
		t.Fatalf("a project layer changed a limit: %+v", got.Checkpoints)
	}
	got = (Settings{}).Override(Settings{Checkpoints: &CheckpointSettings{MaxPerSession: ip(500), RetainDays: ip(365)}})
	if got.CheckpointsMaxPerSession() != 50 || got.CheckpointsRetainDays() != 7 {
		t.Fatalf("a project layer raised a limit: %+v", got.Checkpoints)
	}
	if got.Checkpoints != nil {
		t.Fatalf("an all-ignored project block left a residue: %+v", got.Checkpoints)
	}
}

func TestCheckpointsUserLayersSetAnyKeyInRange(t *testing.T) {
	eff := Effective{Origin: map[string]Source{}}
	eff.apply(Settings{Checkpoints: &CheckpointSettings{MaxPerSession: ip(500), MaxBytes: i64p(10 << 30), RetainDays: ip(365)}}, SourceGlobal)
	s := eff.Settings
	if s.CheckpointsMaxPerSession() != 500 || s.CheckpointsMaxBytes() != 10<<30 || s.CheckpointsRetainDays() != 365 {
		t.Fatalf("user layer lost: %+v", s.Checkpoints)
	}
	if eff.Origin["checkpoints"] != SourceGlobal {
		t.Fatalf("origin = %q", eff.Origin["checkpoints"])
	}
	// An out-of-range value is dropped and the earlier one stays.
	eff.apply(Settings{Checkpoints: &CheckpointSettings{MaxPerSession: ip(9999)}}, SourceGlobal)
	if eff.Settings.CheckpointsMaxPerSession() != 500 {
		t.Fatal("an out-of-range value replaced a good one")
	}
	// A later project layer changes nothing.
	no := false
	eff.apply(Settings{Checkpoints: &CheckpointSettings{RetainDays: ip(1), Enabled: &no}}, SourceProject)
	if eff.Settings.CheckpointsRetainDays() != 365 || eff.Settings.CheckpointsEnabled() {
		t.Fatalf("project layer: %+v", eff.Settings.Checkpoints)
	}
}

func TestResolveDropsProjectCheckpointLimitsWithANote(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()
	yes := true
	if err := SaveProject(workdir, Settings{Checkpoints: &CheckpointSettings{Enabled: &yes, RetainDays: ip(1)}}); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}
	eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if eff.Settings.CheckpointsRetainDays() != 7 {
		t.Fatal("a project shortened the retention")
	}
	if !hasNote(eff.Notes, "project checkpoints.enabled, checkpoints.retain_days ignored") {
		t.Fatalf("no drop note: %v", eff.Notes)
	}
}

func TestCheckpointsDirIsPerWorkdirAndSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BELAI_HOME", home)
	a, err := CheckpointsDir("/work/a", "sess1")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := CheckpointsDir("/work/b", "sess1")
	if a == b {
		t.Fatal("two working directories share a store")
	}
	root, _ := CheckpointsRoot()
	if !strings.HasPrefix(a, root+string(filepath.Separator)) || !strings.HasSuffix(a, "sess1") {
		t.Fatalf("dir = %q under %q", a, root)
	}
	for _, bad := range []string{"", "..", ".", "a/b", "../x"} {
		if _, err := CheckpointsDir("/work/a", bad); err == nil {
			t.Errorf("session id %q accepted", bad)
		}
	}
}

func TestUnderCheckpointsSeesThroughSymlinks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BELAI_HOME", home)
	root, _ := CheckpointsRoot()
	if err := os.MkdirAll(filepath.Join(root, "p", "s"), 0o700); err != nil {
		t.Fatal(err)
	}
	if !UnderCheckpoints(filepath.Join(root, "p", "s", "index.jsonl")) || !UnderCheckpoints(root) {
		t.Fatal("a path in the store was not recognised")
	}
	if UnderCheckpoints(filepath.Join(home, "settings.json")) || UnderCheckpoints(filepath.Join(home, "checkpoints-other")) {
		t.Fatal("a path beside the store was refused")
	}
	link := filepath.Join(t.TempDir(), "l")
	if err := os.Symlink(root, link); err == nil {
		if !UnderCheckpoints(filepath.Join(link, "p")) {
			t.Fatal("a path through a link into the store was not recognised")
		}
	}
}
