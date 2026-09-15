package wire

import (
	"testing"
)

func TestSurfacePath(t *testing.T) {
	cases := []struct {
		s    Surface
		want string
	}{
		{SurfaceOpenAIChat, "/chat/completions"},
		{SurfaceOpenAIResponses, "/responses"},
		{SurfaceAnthropicMessages, "/v1/messages"},
		{Surface("unknown"), ""},
	}
	for _, c := range cases {
		if got := c.s.Path(); got != c.want {
			t.Fatalf("Path(%q) = %q, want %q", c.s, got, c.want)
		}
	}
}

func TestBuildURL(t *testing.T) {
	cases := []struct {
		base string
		s    Surface
		want string
	}{
		{"https://api.openai.com/v1", SurfaceOpenAIChat, "https://api.openai.com/v1/chat/completions"},
		{"https://api.anthropic.com", SurfaceAnthropicMessages, "https://api.anthropic.com/v1/messages"},
		{"https://x.com/", SurfaceOpenAIResponses, "https://x.com/responses"},
	}
	for _, c := range cases {
		if got := BuildURL(c.base, c.s); got != c.want {
			t.Fatalf("BuildURL(%q, %q) = %q, want %q", c.base, c.s, got, c.want)
		}
	}
}

func TestNewAnthropicTextMessage(t *testing.T) {
	m := NewAnthropicTextMessage("user", "hello")
	if m.Role != "user" || m.Content != "hello" {
		t.Fatalf("message = %+v", m)
	}
}

func TestNewAnthropicBlockMessage(t *testing.T) {
	blocks := []AnthropicRequestBlock{{Type: "text", Text: "hi"}}
	m := NewAnthropicBlockMessage("assistant", blocks)
	if m.Role != "assistant" {
		t.Fatalf("role = %q", m.Role)
	}
	content, ok := m.Content.([]AnthropicRequestBlock)
	if !ok || len(content) != 1 {
		t.Fatalf("content = %+v", m.Content)
	}
}
