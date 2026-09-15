package modelinfo

import "testing"

func TestLookupExact(t *testing.T) {
	info, ok := Lookup("gpt-5")
	if !ok {
		t.Fatal("expected gpt-5 to be known")
	}
	if info.ContextWindow != 400_000 {
		t.Fatalf("expected 400k window, got %d", info.ContextWindow)
	}
}

func TestLookupPrefix(t *testing.T) {
	info, ok := Lookup("claude-opus-4-9")
	if !ok {
		t.Fatal("expected prefix match")
	}
	if info.ContextWindow != 1_000_000 {
		t.Fatalf("expected 1M window, got %d", info.ContextWindow)
	}
}

func TestLookupUnknown(t *testing.T) {
	_, ok := Lookup("totally-unknown-model-xyz")
	if ok {
		t.Fatal("expected unknown model")
	}
}

func TestLookupNormalization(t *testing.T) {
	info, ok := Lookup("  GPT-5  ")
	if !ok {
		t.Fatal("expected normalized match")
	}
	if info.ID != "gpt-5" {
		t.Fatalf("expected gpt-5, got %s", info.ID)
	}
}

func TestLookupWorkersAI(t *testing.T) {
	info, ok := Lookup("workers-ai/@cf/moonshotai/kimi-k2.6")
	if !ok {
		t.Fatal("expected workers-ai prefix stripped")
	}
	if info.ID != "@cf/moonshotai/kimi-k2.6" {
		t.Fatalf("unexpected id %s", info.ID)
	}
}

func TestResolveWithOverride(t *testing.T) {
	overrides := map[string]int{"custom": 999}
	v, ok := Resolve("custom", overrides)
	if !ok || v != 999 {
		t.Fatalf("expected override 999, got %d %v", v, ok)
	}
}

func TestResolveFallback(t *testing.T) {
	v, ok := Resolve("gpt-5", nil)
	if !ok || v != 400_000 {
		t.Fatalf("expected fallback 400000, got %d %v", v, ok)
	}
}
