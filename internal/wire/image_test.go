package wire

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestChatMessageWithImagesUsesContentParts(t *testing.T) {
	b, err := json.Marshal(OpenAIChatMessage{Role: "user", Content: "look", Images: []ImageInput{{MediaType: "image/png", Data: []byte("abc")}}})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL struct {
				URL string `json:"url"`
			} `json:"image_url"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	if len(got.Content) != 2 || got.Content[0].Type != "text" || got.Content[0].Text != "look" {
		t.Fatalf("parts: %s", b)
	}
	if got.Content[1].Type != "image_url" || got.Content[1].ImageURL.URL != "data:image/png;base64,YWJj" {
		t.Fatalf("image part: %s", b)
	}
}

func TestChatMessageWithoutImagesIsUnchanged(t *testing.T) {
	cases := map[string]OpenAIChatMessage{
		`{"role":"user","content":"hi"}`:                  {Role: "user", Content: "hi"},
		`{"role":"tool","content":"","tool_call_id":"a"}`: {Role: "tool", ToolCallID: "a"},
		`{"role":"assistant","tool_calls":[{"id":"x","type":"function","function":{"name":"n","arguments":"{}"}}]}`: {
			Role: "assistant", ToolCalls: []OpenAIToolCall{{ID: "x", Type: "function", Function: ToolCallFunction{Name: "n", Arguments: NewStringToolCallArgs("{}")}}},
		},
	}
	for want, m := range cases {
		b, err := json.Marshal(m)
		if err != nil || string(b) != want {
			t.Errorf("got %s (%v), want %s", b, err, want)
		}
	}
}

func TestAnthropicToolResultPartsReplaceContent(t *testing.T) {
	blk := AnthropicRequestBlock{Type: "tool_result", ToolUseID: "t1", Content: "ignored", Parts: []AnthropicRequestBlock{
		{Type: "text", Text: "hello"}, NewAnthropicImageBlock("image/png", []byte("abc")),
	}}
	b, err := json.Marshal(blk)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if strings.Contains(s, "ignored") || !strings.Contains(s, `"content":[{"type":"text","text":"hello"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"YWJj"}}]`) {
		t.Fatalf("wire = %s", s)
	}
	if strings.Count(s, `"content"`) != 1 {
		t.Fatalf("content emitted twice: %s", s)
	}
}

func TestAnthropicBlockWithoutPartsIsUnchanged(t *testing.T) {
	b, _ := json.Marshal(AnthropicRequestBlock{Type: "tool_result", ToolUseID: "t1", Content: "ok"})
	if string(b) != `{"type":"tool_result","content":"ok","tool_use_id":"t1"}` && string(b) != `{"type":"tool_result","tool_use_id":"t1","content":"ok"}` {
		t.Fatalf("wire = %s", b)
	}
}
