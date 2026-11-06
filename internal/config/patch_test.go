package config

import (
	"encoding/json"
	"testing"
)

func TestPatchDefaults(t *testing.T) {
	var s Settings
	if !s.PatchToolEnabled() {
		t.Fatal("patch_tool must default to on")
	}
	if got := s.PatchToleranceValue(); got != PatchWhitespace {
		t.Fatalf("default patch_tolerance = %q, want whitespace", got)
	}
}

func TestPatchTolerancePicksStrictForAnUnknownWord(t *testing.T) {
	if got := (Settings{PatchTolerance: "loose"}).PatchToleranceValue(); got != PatchStrict {
		t.Fatalf("a typo widened the tool: %q", got)
	}
	if PatchToleranceRank("loose") != 0 || PatchToleranceRank(PatchUnicode) != 2 {
		t.Fatal("rank order wrong")
	}
}

func TestPatchSettingsParse(t *testing.T) {
	var s Settings
	if err := json.Unmarshal([]byte(`{"patch_tool":false,"patch_tolerance":"unicode"}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.PatchToolEnabled() || s.PatchToleranceValue() != PatchUnicode {
		t.Fatalf("parsed %+v", s)
	}
}

func TestMergePatchToolProjectOnlyTurnsOff(t *testing.T) {
	on, off := ptr(true), ptr(false)
	if got := mergePatchTool(nil, on, true); got != nil {
		t.Fatal("a project true changed the default")
	}
	if got := mergePatchTool(off, on, true); got != off {
		t.Fatal("a project layer turned the tool back on")
	}
	if got := mergePatchTool(nil, off, true); got == nil || *got {
		t.Fatal("a project layer could not turn the tool off")
	}
	if got := mergePatchTool(off, on, false); got == nil || !*got {
		t.Fatal("a user layer must be able to turn it on")
	}
	if got := mergePatchTool(off, nil, true); got != off {
		t.Fatal("a nil layer changed the value")
	}
}

func TestMergePatchToleranceProjectOnlyTightens(t *testing.T) {
	cases := []struct {
		name, cur, in string
		project       bool
		want          string
	}{
		{"project unicode over default", "", PatchUnicode, true, ""},
		{"project whitespace over default", "", PatchWhitespace, true, ""},
		{"project strict over default", "", PatchStrict, true, PatchStrict},
		{"project strict over user unicode", PatchUnicode, PatchStrict, true, PatchStrict},
		{"project whitespace over user unicode", PatchUnicode, PatchWhitespace, true, PatchWhitespace},
		{"project unicode over user whitespace", PatchWhitespace, PatchUnicode, true, PatchWhitespace},
		{"project unicode over user strict", PatchStrict, PatchUnicode, true, PatchStrict},
		{"project typo is strict", PatchUnicode, "loose", true, PatchStrict},
		{"project empty changes nothing", PatchWhitespace, "", true, PatchWhitespace},
		{"user unicode", "", PatchUnicode, false, PatchUnicode},
		{"user loosens over its own strict", PatchStrict, PatchUnicode, false, PatchUnicode},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mergePatchTolerance(c.cur, c.in, c.project); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestOverrideProjectCannotLoosenPatchSettings(t *testing.T) {
	global := Settings{PatchTool: ptr(false), PatchTolerance: PatchStrict}
	got := global.Override(Settings{PatchTool: ptr(true), PatchTolerance: PatchUnicode})
	if got.PatchToolEnabled() || got.PatchToleranceValue() != PatchStrict {
		t.Fatalf("a project layer loosened %+v", got)
	}
	got = (Settings{}).Override(Settings{PatchTool: ptr(false), PatchTolerance: PatchStrict})
	if got.PatchToolEnabled() || got.PatchToleranceValue() != PatchStrict {
		t.Fatalf("a project layer could not narrow %+v", got)
	}
}

func TestResolvePatchSettingsProjectNarrowsOnly(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()
	if err := SaveGlobal(Settings{PatchTool: ptr(true), PatchTolerance: PatchUnicode}); err != nil {
		t.Fatal(err)
	}
	if err := SaveProject(workdir, Settings{PatchTool: ptr(true), PatchTolerance: PatchUnicode}); err != nil {
		t.Fatal(err)
	}
	eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if !eff.Settings.PatchToolEnabled() || eff.Settings.PatchToleranceValue() != PatchUnicode || eff.Origin["patch_tolerance"] != SourceGlobal {
		t.Fatalf("user layer not honoured: %+v origin %v", eff.Settings, eff.Origin["patch_tolerance"])
	}

	if err := SaveProject(workdir, Settings{PatchTool: ptr(false), PatchTolerance: PatchStrict}); err != nil {
		t.Fatal(err)
	}
	eff, err = Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if eff.Settings.PatchToolEnabled() || eff.Settings.PatchToleranceValue() != PatchStrict {
		t.Fatalf("project narrowing not honoured: %+v", eff.Settings)
	}
	if eff.Origin["patch_tool"] != SourceProject || eff.Origin["patch_tolerance"] != SourceProject {
		t.Fatalf("origins %v %v", eff.Origin["patch_tool"], eff.Origin["patch_tolerance"])
	}

	if err := SaveGlobal(Settings{PatchTool: ptr(false), PatchTolerance: PatchStrict}); err != nil {
		t.Fatal(err)
	}
	if err := SaveProject(workdir, Settings{PatchTool: ptr(true), PatchTolerance: PatchUnicode}); err != nil {
		t.Fatal(err)
	}
	eff, err = Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if eff.Settings.PatchToolEnabled() || eff.Settings.PatchToleranceValue() != PatchStrict {
		t.Fatalf("a project layer loosened the user's settings: %+v", eff.Settings)
	}
}
