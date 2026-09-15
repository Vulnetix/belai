package models

import "testing"

func TestCatalogOpenAI(t *testing.T) {
	c := Catalog("openai")
	if len(c) == 0 {
		t.Fatal("expected non-empty openai catalog")
	}
	if c[0].ID != "gpt-5" {
		t.Fatalf("expected gpt-5 first, got %s", c[0].ID)
	}
}

func TestCatalogAnthropic(t *testing.T) {
	c := Catalog("anthropic")
	found := false
	for _, m := range c {
		if m.ID == "claude-opus-4-5" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected claude-opus-4-5 in anthropic catalog")
	}
}

func TestCatalogUnknownProvider(t *testing.T) {
	c := Catalog("unknown")
	if len(c) == 0 {
		t.Fatal("expected default openai catalog for unknown provider")
	}
}

func TestEfforts(t *testing.T) {
	e := Efforts("openai", "gpt-5")
	if len(e) != 3 {
		t.Fatalf("expected 3 effort levels, got %d", len(e))
	}
}

func TestEffortsUnknownModel(t *testing.T) {
	e := Efforts("openai", "unknown")
	if len(e) != 3 {
		t.Fatalf("expected default efforts for unknown model, got %d", len(e))
	}
}

func TestThinkingBudget(t *testing.T) {
	if v := ThinkingBudget("low"); v != 1024 {
		t.Fatalf("expected 1024, got %d", v)
	}
	if v := ThinkingBudget("medium"); v != 4096 {
		t.Fatalf("expected 4096, got %d", v)
	}
	if v := ThinkingBudget("high"); v != 16384 {
		t.Fatalf("expected 16384, got %d", v)
	}
	if v := ThinkingBudget(""); v != 0 {
		t.Fatalf("expected 0, got %d", v)
	}
}

func TestLabel(t *testing.T) {
	if v := Label("openai", "gpt-5"); v != "GPT-5" {
		t.Fatalf("expected GPT-5, got %s", v)
	}
	if v := Label("openai", "unknown"); v != "unknown" {
		t.Fatalf("expected unknown, got %s", v)
	}
}
