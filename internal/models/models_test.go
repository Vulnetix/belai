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

func TestCatalogUnknownProviderIsEmpty(t *testing.T) {
	if cat := Catalog("bogus"); cat != nil {
		t.Fatalf("unknown provider should have no built-in catalog, got %+v", cat)
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

func TestCatalogOllamaIsEmpty(t *testing.T) {
	if cat := Catalog("ollama"); cat != nil {
		t.Fatalf("ollama catalog should be empty, got %+v", cat)
	}
}

func TestCatalogHuggingFace(t *testing.T) {
	cat := Catalog("huggingface")
	if len(cat) == 0 {
		t.Fatal("huggingface catalog should not be empty")
	}
	for _, m := range cat {
		if m.ID == "" || m.Label == "" || len(m.Efforts) == 0 {
			t.Fatalf("huggingface catalog has incomplete model %+v", m)
		}
	}
	if got := Label("huggingface", "meta-llama/Llama-3.1-8B-Instruct"); got != "Llama 3.1 8B" {
		t.Fatalf("label = %q", got)
	}
}
