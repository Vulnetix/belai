package modelinfo

import "testing"

func TestExactHit(t *testing.T) {
	info, ok := Lookup("gpt-5")
	if !ok || info.ContextWindow != 400_000 || info.MaxOutput != 128_000 {
		t.Fatalf("Lookup(gpt-5) = %+v, %v", info, ok)
	}
}

func TestCaseFoldAndTrim(t *testing.T) {
	info, ok := Lookup("  GPT-5 ")
	if !ok || info.ContextWindow != 400_000 {
		t.Fatalf("Lookup = %+v, %v", info, ok)
	}
}

func TestDotDashEquivalence(t *testing.T) {
	dashed, ok1 := Lookup("claude-sonnet-4-5")
	dotted, ok2 := Lookup("claude-sonnet-4.5")
	if !ok1 || !ok2 {
		t.Fatalf("both forms must resolve")
	}
	if dashed.ContextWindow != dotted.ContextWindow || dashed.ContextWindow != 1_000_000 {
		t.Fatalf("dot/dash forms diverged: %+v vs %+v", dashed, dotted)
	}
}

func TestWorkersAIPrefixStrip(t *testing.T) {
	info, ok := Lookup("workers-ai/@cf/moonshotai/kimi-k2.6")
	if !ok || info.ContextWindow != 262_144 {
		t.Fatalf("Lookup = %+v, %v", info, ok)
	}
}

func TestExactBeatsPrefix(t *testing.T) {
	info, ok := Lookup("claude-opus-4-5")
	if !ok || info.ContextWindow != 200_000 {
		t.Fatalf("claude-opus-4-5 = %+v, want 200k (not the 1M family prefix)", info)
	}
}

func TestLongestPrefixWins(t *testing.T) {
	info, ok := Lookup("gpt-4.1-custom-variant")
	if !ok || info.ContextWindow != 1_047_576 {
		t.Fatalf("gpt-4.1 prefix should win, got %+v", info)
	}
}

func TestUnknownReturnsFalse(t *testing.T) {
	if _, ok := Lookup("no-such-model-xyz"); ok {
		t.Fatalf("unknown model must return ok=false")
	}
}

func TestResolveHonoursOverride(t *testing.T) {
	got, ok := Resolve("my-model", map[string]int{"my-model": 12345})
	if !ok || got != 12345 {
		t.Fatalf("Resolve = %d, %v", got, ok)
	}
}

func TestResolveNilSafe(t *testing.T) {
	got, ok := Resolve("gpt-5", nil)
	if !ok || got != 400_000 {
		t.Fatalf("Resolve = %d, %v", got, ok)
	}
	if _, ok := Resolve("unknown", nil); ok {
		t.Fatalf("unknown with nil overrides must return ok=false")
	}
}

func TestAllDefaultModelsCovered(t *testing.T) {
	for _, id := range []string{"gpt-5", "claude-sonnet-4-5", "claude-opus-4-5", "@cf/moonshotai/kimi-k2.6"} {
		if _, ok := Lookup(id); !ok {
			t.Fatalf("default model %q must be in the registry", id)
		}
	}
}
