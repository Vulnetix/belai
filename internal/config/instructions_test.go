package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

func TestInstructionsDefaults(t *testing.T) {
	for _, s := range []Settings{{}, {Instructions: &InstructionsSettings{}}} {
		if !s.InstructionFilesEnabled() || !s.InstructionFilesNested() ||
			s.InstructionMaxFiles() != 12 || s.InstructionFileBytes() != 49152 || s.InstructionTotalBytes() != 131072 {
			t.Fatalf("defaults wrong for %+v", s.Instructions)
		}
	}
}

func TestInstructionsNestedNeedsEnabled(t *testing.T) {
	s := Settings{Instructions: &InstructionsSettings{Enabled: bp(false)}}
	if s.InstructionFilesEnabled() || s.InstructionFilesNested() {
		t.Fatal("enabled=false must also stop nested delivery")
	}
	s = Settings{Instructions: &InstructionsSettings{Nested: bp(false)}}
	if !s.InstructionFilesEnabled() || s.InstructionFilesNested() {
		t.Fatal("nested=false keeps the root files")
	}
}

func TestInstructionsOutOfRangeFailsValidationAndClampsToDefault(t *testing.T) {
	bad := []struct {
		name string
		c    InstructionsSettings
		want string
	}{
		{"files zero", InstructionsSettings{MaxFiles: ip(0)}, "instructions.max_files"},
		{"files high", InstructionsSettings{MaxFiles: ip(41)}, "instructions.max_files"},
		{"file bytes low", InstructionsSettings{FileBytes: ip(1023)}, "instructions.file_bytes"},
		{"file bytes high", InstructionsSettings{FileBytes: ip(256*1024 + 1)}, "instructions.file_bytes"},
		{"total low", InstructionsSettings{TotalBytes: ip(10)}, "instructions.total_bytes"},
		{"total high", InstructionsSettings{TotalBytes: ip(1024*1024 + 1)}, "instructions.total_bytes"},
	}
	for _, tc := range bad {
		s := Settings{Instructions: &tc.c}
		if err := ValidateInstructions(s); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to name %s", tc.name, err, tc.want)
		}
		if err := ValidateSettings(s); err == nil {
			t.Errorf("%s: ValidateSettings accepted it", tc.name)
		}
		if s.InstructionMaxFiles() != 12 || s.InstructionFileBytes() != 49152 || s.InstructionTotalBytes() != 131072 {
			t.Errorf("%s: a bad value reached the code", tc.name)
		}
	}
}

func TestInstructionsUserLayerMayRaiseToTheCeilings(t *testing.T) {
	cur := &InstructionsSettings{}
	in := &InstructionsSettings{MaxFiles: ip(40), FileBytes: ip(256 * 1024), TotalBytes: ip(1024 * 1024), Enabled: bp(true), Nested: bp(false)}
	s := Settings{Instructions: mergeInstructions(cur, in, false)}
	if s.InstructionMaxFiles() != 40 || s.InstructionFileBytes() != 256*1024 || s.InstructionTotalBytes() != 1024*1024 || s.InstructionFilesNested() {
		t.Fatalf("user layer not honoured: %+v", s.Instructions)
	}
}

func TestInstructionsProjectCanOnlyNarrow(t *testing.T) {
	user := &InstructionsSettings{MaxFiles: ip(20), Enabled: bp(false), Nested: bp(false)}
	// Turning on and raising are dropped.
	got := mergeInstructions(user, &InstructionsSettings{Enabled: bp(true), Nested: bp(true), MaxFiles: ip(30), FileBytes: ip(200000), TotalBytes: ip(900000)}, true)
	s := Settings{Instructions: got}
	if s.InstructionFilesEnabled() || s.InstructionFilesNested() {
		t.Fatal("a project turned a switch on")
	}
	if s.InstructionMaxFiles() != 20 || s.InstructionFileBytes() != 49152 || s.InstructionTotalBytes() != 131072 {
		t.Fatalf("a project raised a cap: %+v", got)
	}
	// Turning off and lowering are applied.
	got = mergeInstructions(nil, &InstructionsSettings{Enabled: bp(false), Nested: bp(false), MaxFiles: ip(3), FileBytes: ip(2048), TotalBytes: ip(4096)}, true)
	s = Settings{Instructions: got}
	if s.InstructionFilesEnabled() || s.InstructionFilesNested() || s.InstructionMaxFiles() != 3 || s.InstructionFileBytes() != 2048 || s.InstructionTotalBytes() != 4096 {
		t.Fatalf("a project could not narrow: %+v", got)
	}
	// An out-of-range project value is dropped, not applied and not an error.
	got = mergeInstructions(nil, &InstructionsSettings{MaxFiles: ip(0), FileBytes: ip(-5)}, true)
	if got.MaxFiles != nil || got.FileBytes != nil {
		t.Fatalf("out-of-range project value kept: %+v", got)
	}
	// An equal value changes nothing.
	got = mergeInstructions(nil, &InstructionsSettings{MaxFiles: ip(12)}, true)
	if got.MaxFiles != nil {
		t.Fatalf("a no-op project value was stored: %+v", got)
	}
}

func TestInstructionsOverrideNarrowsOnly(t *testing.T) {
	user := Settings{Instructions: &InstructionsSettings{MaxFiles: ip(30)}}
	out := user.Override(Settings{Instructions: &InstructionsSettings{MaxFiles: ip(40), FileBytes: ip(4096), Nested: bp(true)}})
	if out.InstructionMaxFiles() != 30 || out.InstructionFileBytes() != 4096 || !out.InstructionFilesNested() {
		t.Fatalf("Override: %+v", out.Instructions)
	}
}

func TestInstructionsJSONRoundTrip(t *testing.T) {
	var s Settings
	if err := json.Unmarshal([]byte(`{"instructions":{"enabled":false,"max_files":5}}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.InstructionFilesEnabled() || s.InstructionMaxFiles() != 5 {
		t.Fatalf("%+v", s.Instructions)
	}
	if err := json.Unmarshal([]byte(`{"instructions":{"enabled":false,"bogus":1}}`), &Settings{}); err != nil {
		// unknown keys are the strict loader's concern; plain Unmarshal ignores them
		t.Logf("unknown key: %v", err)
	}
}

func TestResolveInstructionsProjectLayer(t *testing.T) {
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
	if err := SaveGlobal(Settings{Instructions: &InstructionsSettings{Enabled: bp(false), MaxFiles: ip(20)}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveProject(workdir, Settings{Instructions: &InstructionsSettings{Enabled: bp(true), MaxFiles: ip(30)}}); err != nil {
		t.Fatal(err)
	}
	eff := resolve()
	if eff.Settings.InstructionFilesEnabled() || eff.Settings.InstructionMaxFiles() != 20 {
		t.Fatalf("project widened: %+v", eff.Settings.Instructions)
	}
	noted := false
	for _, n := range eff.Notes {
		if strings.Contains(n, "instructions.enabled") && strings.Contains(n, "instructions.max_files") {
			noted = true
		}
	}
	if !noted {
		t.Fatalf("no note for the ignored project keys: %v", eff.Notes)
	}
	if err := SaveProject(workdir, Settings{Instructions: &InstructionsSettings{MaxFiles: ip(5)}}); err != nil {
		t.Fatal(err)
	}
	eff = resolve()
	if eff.Settings.InstructionMaxFiles() != 5 || eff.Origin["instructions"] != SourceProject {
		t.Fatalf("project lowering not applied: %d %v", eff.Settings.InstructionMaxFiles(), eff.Origin["instructions"])
	}
	// A user layer with an out-of-range value fails the load.
	if err := SaveProject(workdir, Settings{}); err != nil {
		t.Fatal(err)
	}
	if err := SaveGlobal(Settings{Instructions: &InstructionsSettings{MaxFiles: ip(99)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(workdir, func(string) string { return "" }, Settings{}); err == nil {
		t.Fatal("an out-of-range user value was accepted")
	}
}

// TestInstructionFilesPageStatesEachDefaultAndRange keeps the settings table in
// docs/instruction-files.md equal to the table the code validates against.
func TestInstructionFilesPageStatesEachDefaultAndRange(t *testing.T) {
	doc := docparity.Read(t, "docs/instruction-files.md")
	for _, f := range instructionsFields {
		row := fmt.Sprintf("| `%s` | %d | %d to %d |", f.key, f.def, f.min, f.max)
		if !strings.Contains(doc, row) {
			t.Errorf("docs/instruction-files.md does not state %q", row)
		}
	}
	for _, want := range []string{"| `instructions.enabled` | on |", "| `instructions.nested` | on |"} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/instruction-files.md does not state %q", want)
		}
	}
}
