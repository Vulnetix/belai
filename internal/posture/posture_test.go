package posture

import (
	"testing"
)

func TestDefaults(t *testing.T) {
	p := Defaults()
	for _, g := range AllGates {
		if p.Level(g) != Enforce {
			t.Fatalf("gate %q expected enforce, got %q", g, p.Level(g))
		}
	}
}

func TestLevelDefault(t *testing.T) {
	var p Policy
	if p.Level(ToolResultUnsafe) != Enforce {
		t.Fatalf("nil policy should default to enforce")
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
		t.Fatalf("expected 2 downgrades, got %d", len(d))
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
