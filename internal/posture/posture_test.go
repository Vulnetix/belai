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

func TestPrintBanner(t *testing.T) {
	p := Defaults().Override(Policy{ToolResultUnsafe: Warn})
	PrintBanner(p, os.Stderr)
}
