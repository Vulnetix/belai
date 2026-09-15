package models

import "testing"

func TestCatalogReturnsModels(t *testing.T) {
	for _, p := range []string{"openai", "anthropic", "cloudflare-workers-ai", "cloudflare-ai-gateway"} {
		cat := Catalog(p)
		if len(cat) == 0 {
			t.Fatalf("Catalog(%s) is empty", p)
		}
		for _, m := range cat {
			if m.ID == "" || m.Label == "" || len(m.Efforts) == 0 {
				t.Fatalf("Catalog(%s) has incomplete model %+v", p, m)
			}
		}
	}
}

func TestCatalogUnknownProviderDefaultsToOpenAI(t *testing.T) {
	cat := Catalog("bogus")
	if len(cat) == 0 || cat[0].ID != "gpt-5" {
		t.Fatalf("unknown provider should default to openai catalog, got %+v", cat)
	}
}

func TestEffortsFallsBack(t *testing.T) {
	if got := Efforts("openai", "not-a-model"); len(got) != 3 {
		t.Fatalf("Efforts fallback = %v", got)
	}
	if got := Efforts("openai", "gpt-5"); len(got) == 0 {
		t.Fatalf("known model should have efforts")
	}
}

func TestThinkingBudget(t *testing.T) {
	cases := map[string]int{"low": 1024, "medium": 4096, "high": 16384, "": 0, "bogus": 0}
	for effort, want := range cases {
		if got := ThinkingBudget(effort); got != want {
			t.Fatalf("ThinkingBudget(%q) = %d, want %d", effort, got, want)
		}
	}
}

func TestLabel(t *testing.T) {
	if got := Label("openai", "gpt-5"); got != "GPT-5" {
		t.Fatalf("Label = %q", got)
	}
	if got := Label("openai", "unknown-id"); got != "unknown-id" {
		t.Fatalf("Label fallback = %q", got)
	}
}
