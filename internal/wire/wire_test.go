package wire

import (
	"encoding/json"
	"strings"
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

// An OpenAI-compatible server validates a user/system/tool message against a
// schema that requires "content". Omitting the field on an empty message makes
// the server fall through to a schema that forbids the role, and the request
// fails with a 400 naming both errors, so an empty content field must still be
// sent as "".
func TestChatMessageKeepsEmptyContentForContentRoles(t *testing.T) {
	for _, role := range []string{"user", "system", "tool"} {
		b, err := json.Marshal(OpenAIChatMessage{Role: role})
		if err != nil {
			t.Fatalf("Marshal(%s): %v", role, err)
		}
		var got map[string]any
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("Unmarshal(%s): %v", role, err)
		}
		content, ok := got["content"]
		if !ok {
			t.Fatalf("role %q marshalled without a content field: %s", role, b)
		}
		if content != "" {
			t.Fatalf("role %q content = %v, want the empty string", role, content)
		}
	}
}

func TestEphemeralCache(t *testing.T) {
	c := EphemeralCache()
	if c == nil || c.Type != "ephemeral" {
		t.Fatalf("EphemeralCache = %+v, want type ephemeral", c)
	}
}

// An assistant message carrying tool calls is the one case where the content
// field is legitimately absent; providers reject an empty assistant content
// alongside tool_calls.
func TestAssistantWithToolCallsOmitsEmptyContent(t *testing.T) {
	b, err := json.Marshal(OpenAIChatMessage{
		Role:      "assistant",
		ToolCalls: []OpenAIToolCall{{ID: "1", Type: "function"}},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(b), `"content"`) {
		t.Fatalf("assistant tool-call message carries a content field: %s", b)
	}
}
