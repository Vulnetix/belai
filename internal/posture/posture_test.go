package posture

import (
	"testing"
)

func TestDefaults(t *testing.T) {
	p := Defaults()
	for _, g := range AllGates {
		if p.Level(g) != DefaultLevel(g) {
			t.Fatalf("gate %q expected default %q, got %q", g, DefaultLevel(g), p.Level(g))
		}
	}
	if p.Level(PermissionNoMatch) != Ignore {
		t.Fatalf("permission_no_match should default to ignore, got %q", p.Level(PermissionNoMatch))
	}
}

func TestLevelDefault(t *testing.T) {
	var p Policy
	if p.Level(ToolResultUnsafe) != Enforce {
		t.Fatalf("nil policy should default to enforce")
	}
	if p.Level(PermissionNoMatch) != Ignore {
		t.Fatalf("nil policy should default permission_no_match to ignore, got %q", p.Level(PermissionNoMatch))
	}
}

func TestOverride(t *testing.T) {
	p := Defaults()
	q := Policy{ToolResultUnsafe: Warn, SkillInvalid: Ignore}
	out := p.Override(q)
	if out.Level(ToolResultUnsafe) != Warn {
		t.Fatalf("override failed")
	}
	if out.Level(SkillInvalid) != Ignore {
		t.Fatalf("override failed")
	}
	if out.Level(ToolResultMalformed) != Enforce {
		t.Fatalf("non-overridden gate should stay enforce")
	}
}

func TestDowngrades(t *testing.T) {
	p := Policy{ToolResultUnsafe: Warn, SkillInvalid: Ignore}
	d := p.Downgrades()
	if len(d) != 2 {
		t.Fatalf("expected 2 downgrades, got %d: %v", len(d), d)
	}
}

// The default policy must not emit banner noise: every gate sits at its
// default level, including permission_no_match=ignore.
func TestDefaultsNoDowngradeBanner(t *testing.T) {
	if d := Defaults().Downgrades(); len(d) != 0 {
		t.Fatalf("expected no downgrades from defaults, got %v", d)
	}
}

func TestFlagSetToPolicy(t *testing.T) {
	allow := true
	strip := "strip"
	fs := FlagSet{
		AllowUnsafeToolResult: &allow,
		ToolCallMismatch:      &strip,
	}
	p := fs.ToPolicy()
	if p.Level(ToolResultUnsafe) != Ignore {
		t.Fatalf("expected ignore for allow-unsafe-tool-result")
	}
	if p.Level(ToolCallMismatch) != Warn {
		t.Fatalf("expected warn for mismatch=strip")
	}
	if p.Level(PromptUnsafe) != Enforce {
		t.Fatalf("expected enforce for untouched gate")
	}
}

func TestDangerouslyYolo(t *testing.T) {
	fs := FlagSet{DangerouslyYolo: true}
	p := fs.ToPolicy()
	for _, g := range AllGates {
		if p.Level(g) != Ignore {
			t.Fatalf("expected ignore for all gates under yolo, got %q for %q", p.Level(g), g)
		}
	}
}
