package models

import "testing"

func TestVision(t *testing.T) {
	yes := []string{
		"claude-opus-5", "claude-sonnet-5-5", "claude-haiku-4-5-20251001", "claude-fable-5-1",
		"anthropic/claude-sonnet-4-6", "claude-3-5-sonnet-20241022",
		"gpt-4o", "gpt-4o-mini", "gpt-4.1", "gpt-5", "openai/gpt-5.2", "o3", "o4-mini",
		"gemini-2.5-pro", "google/gemini-2.5-flash", "pixtral-large-latest",
	}
	no := []string{
		"", "o1-mini", "gpt-3.5-turbo", "deepseek-chat", "deepseek-reasoner",
		"llama-3.3-70b-versatile", "mistral-small-latest", "kimi-k2-0711", "some-local-model",
	}
	for _, id := range yes {
		if !Vision("p", id) {
			t.Errorf("Vision(%q) = false, want true", id)
		}
	}
	for _, id := range no {
		if Vision("p", id) {
			t.Errorf("Vision(%q) = true, want false", id)
		}
	}
}

func TestVisionIsCaseInsensitive(t *testing.T) {
	if !Vision("p", "  Claude-Opus-5 ") {
		t.Fatal("ids should be matched case-insensitively and trimmed")
	}
}
