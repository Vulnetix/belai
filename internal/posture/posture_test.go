package posture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultLevel(t *testing.T) {
	if DefaultLevel(ToolResultUnsafe) != Enforce {
		t.Fatal("ToolResultUnsafe should default to enforce")
	}
	if DefaultLevel(PermissionNoMatch) != Ignore {
		t.Fatal("PermissionNoMatch should default to ignore")
	}
}

func TestPolicyLevel(t *testing.T) {
	p := Policy{ToolResultUnsafe: Warn}
	if p.Level(ToolResultUnsafe) != Warn {
		t.Fatal("expected warn")
	}
	if p.Level(ToolResultMalformed) != Enforce {
		t.Fatal("expected default enforce")
	}
}

func TestPolicyLevelNil(t *testing.T) {
	var p Policy
	if p.Level(PermissionNoMatch) != Ignore {
		t.Fatal("nil policy should return default")
	}
}

func TestDefaults(t *testing.T) {
	p := Defaults()
	for _, g := range AllGates {
		if _, ok := p[g]; !ok {
			t.Fatalf("missing gate %s", g)
		}
	}
}

func TestOverride(t *testing.T) {
	p := Defaults().Override(Policy{ToolResultUnsafe: Ignore})
	if p.Level(ToolResultUnsafe) != Ignore {
		t.Fatal("override should win")
	}
}

func TestOverrideIgnoresEmpty(t *testing.T) {
	p := Policy{ToolResultUnsafe: Warn}.Override(Policy{ToolResultUnsafe: ""})
	if p.Level(ToolResultUnsafe) != Warn {
		t.Fatal("empty override should not replace")
	}
}

func TestDowngrades(t *testing.T) {
	p := Defaults().Override(Policy{ToolResultUnsafe: Warn})
	down := p.Downgrades()
	found := false
	for _, d := range down {
		if strings.Contains(d, "tool_result_unsafe") {
			found = true
		}
	}
	if !found {
		t.Fatalf("downgrades missing tool_result_unsafe: %v", down)
	}
}

func TestIsLessStrict(t *testing.T) {
	if !isLessStrict(Ignore, Enforce) {
		t.Fatal("ignore is less strict than enforce")
	}
	if !isLessStrict(Warn, Enforce) {
		t.Fatal("warn is less strict than enforce")
	}
	if isLessStrict(Enforce, Enforce) {
		t.Fatal("enforce is not less strict than enforce")
	}
}

func TestFlagSetToPolicyUnsafeToolResult(t *testing.T) {
	truePtr := func(b bool) *bool { return &b }
	fs := FlagSet{AllowUnsafeToolResult: truePtr(true)}
	p := fs.ToPolicy()
	if p.Level(ToolResultUnsafe) != Ignore {
		t.Fatal("expected ignore")
	}
}

func TestFlagSetToPolicyToolCallMismatchStrip(t *testing.T) {
	strip := "strip"
	fs := FlagSet{ToolCallMismatch: &strip}
	p := fs.ToPolicy()
	if p.Level(ToolCallMismatch) != Warn {
		t.Fatal("strip should map to warn")
	}
}

func TestFlagSetToPolicyToolCallMismatchIgnore(t *testing.T) {
	ignore := "ignore"
	fs := FlagSet{ToolCallMismatch: &ignore}
	p := fs.ToPolicy()
	if p.Level(ToolCallMismatch) != Ignore {
		t.Fatal("ignore should map to ignore")
	}
}

func TestFlagSetToPolicyDangerouslyYolo(t *testing.T) {
	fs := FlagSet{DangerouslyYolo: true}
	p := fs.ToPolicy()
	for _, g := range AllGates {
		if p.Level(g) != Ignore {
			t.Fatalf("gate %s not ignore", g)
		}
	}
}

func TestFlagSetToPolicyDefaults(t *testing.T) {
	fs := FlagSet{}
	p := fs.ToPolicy()
	if len(p) != 0 {
		t.Fatalf("expected empty policy, got %v", p)
	}
}

func TestLoadPreferencesPathMissing(t *testing.T) {
	p, err := loadPreferencesPath("/does/not/exist/preferences.yaml")
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if p != nil && len(p) != 0 {
		t.Fatalf("expected nil/empty, got %v", p)
	}
}

func TestLoadPreferencesPathValid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "preferences.yaml")
	content := `postures:
  tool_result_unsafe: warn
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := loadPreferencesPath(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if p.Level(ToolResultUnsafe) != Warn {
		t.Fatalf("expected warn, got %v", p)
	}
}

func TestLoadPreferencesPathInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "preferences.yaml")
	if err := os.WriteFile(path, []byte("not: yaml: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadPreferencesPath(path)
	if err == nil {
		t.Fatal("expected yaml parse error")
	}
}

func TestStricterTakesStrongestLevel(t *testing.T) {
	a := Defaults().Override(Policy{ToolResultUnsafe: Ignore})
	b := Defaults().Override(Policy{ToolResultUnsafe: Warn, PromptUnsafe: Warn})
	p := Stricter(a, b)
	if p.Level(ToolResultUnsafe) != Warn {
		t.Fatalf("ToolResultUnsafe = %q, want warn", p.Level(ToolResultUnsafe))
	}
	if p.Level(PromptUnsafe) != Enforce {
		t.Fatalf("PromptUnsafe = %q, want enforce", p.Level(PromptUnsafe))
	}
	if p.Level(PromptMalformed) != Enforce {
		t.Fatalf("PromptMalformed = %q, want enforce", p.Level(PromptMalformed))
	}
}

func TestForDirsStricterThanPrimary(t *testing.T) {
	dir := t.TempDir()
	projDir := filepath.Join(dir, "project")
	extraDir := filepath.Join(dir, "extra")
	_ = os.MkdirAll(filepath.Join(projDir, ".belai"), 0o755)
	_ = os.MkdirAll(filepath.Join(extraDir, ".belai"), 0o755)

	// Primary is at defaults; extra directory forces tool_result_unsafe to warn.
	_ = os.WriteFile(filepath.Join(extraDir, ".belai", preferencesFile), []byte("postures:\n  tool_result_unsafe: warn\n"), 0o600)

	p, err := ForDirs(projDir, []string{extraDir})
	if err != nil {
		t.Fatalf("ForDirs: %v", err)
	}
	if p.Level(ToolResultUnsafe) != Enforce {
		t.Fatalf("ToolResultUnsafe = %q, want enforce", p.Level(ToolResultUnsafe))
	}
}

func TestForDirs(t *testing.T) {
}

func TestPrintBanner(t *testing.T) {
	p := Defaults().Override(Policy{ToolResultUnsafe: Warn})
	PrintBanner(p, os.Stderr)
}

// The gate/default table is documented in docs/role-manager.md; this keeps the
// two from drifting. Every gate defaults to enforce except permission_no_match
// (the permission layer allows unmatched calls).
func TestGateDefaultsMatchDocumentedTable(t *testing.T) {
	want := map[Gate]Level{
		ToolResultUnsafe:    Enforce,
		ToolResultMalformed: Enforce,
		PromptUnsafe:        Enforce,
		PromptMalformed:     Enforce,
		ToolCallMismatch:    Enforce,
		PermissionNoMatch:   Ignore,
		PermissionAskNoTTY:  Enforce,
		SkillInvalid:        Enforce,
		HookInvalid:         Enforce,
	}
	if len(AllGates) != len(want) {
		t.Fatalf("AllGates has %d gates, documented table has %d", len(AllGates), len(want))
	}
	for _, g := range AllGates {
		w, ok := want[g]
		if !ok {
			t.Fatalf("gate %q is not in the documented table", g)
		}
		if got := DefaultLevel(g); got != w {
			t.Fatalf("DefaultLevel(%q) = %q, want %q", g, got, w)
		}
	}
}

// The banner is one line naming every downgraded gate, and nothing at all when
// the policy is at or above its defaults.
func TestPrintBannerWritesOneDowngradeLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "banner")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	PrintBanner(Defaults().Override(Policy{ToolResultUnsafe: Warn, SkillInvalid: Ignore}), f)
	f.Close()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(b)
	want := "belai: posture downgrades: tool_result_unsafe=warn, skill_invalid=ignore\n"
	if got != want {
		t.Fatalf("banner = %q, want %q", got, want)
	}
}

func TestPrintBannerSilentAtDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "banner")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	PrintBanner(Defaults(), f)
	f.Close()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(b) != 0 {
		t.Fatalf("banner at defaults = %q, want no output", b)
	}
}

// AllIgnore is the one definition of "the operator turned guardrails off", so
// it has to cover every gate — including any gate added later. A gate missing
// from it is a gate that keeps enforcing after the switch is off.
func TestAllIgnoreCoversEveryGate(t *testing.T) {
	p := AllIgnore()
	if len(p) != len(AllGates) {
		t.Fatalf("AllIgnore has %d entries, want %d (one per gate)", len(p), len(AllGates))
	}
	for _, g := range AllGates {
		if got := p.Level(g); got != Ignore {
			t.Errorf("AllIgnore gate %q = %q, want ignore", g, got)
		}
	}
}

// It returns a fresh map each time: a caller that mutates one policy must not
// change what the next caller gets.
func TestAllIgnoreIsNotShared(t *testing.T) {
	a := AllIgnore()
	a[ToolResultUnsafe] = Enforce
	if AllIgnore().Level(ToolResultUnsafe) != Ignore {
		t.Fatal("AllIgnore returned a shared map")
	}
}

// Every gate off is a downgrade of every gate that defaults to enforce, so the
// startup banner has something to report.
func TestAllIgnoreReportsDowngrades(t *testing.T) {
	if len(AllIgnore().Downgrades()) == 0 {
		t.Fatal("AllIgnore reported no downgrades")
	}
}
