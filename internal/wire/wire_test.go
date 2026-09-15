package wire

import (
	"encoding/json"
	"testing"
)

func TestAnthropicTextMessageMarshalsAsString(t *testing.T) {
	msg := NewAnthropicTextMessage("user", "hello")
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"role":"user","content":"hello"}`
	if string(b) != want {
		t.Fatalf("got %s, want %s", b, want)
	}
}

func TestOpenAIChatMessageToolFieldsOmitEmpty(t *testing.T) {
	msg := OpenAIChatMessage{Role: "user", Content: "hi"}
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"role":"user","content":"hi"}`
	if string(b) != want {
		t.Fatalf("got %s, want %s", b, want)
	}
}

func TestAnthropicMessagesRequestToolFields(t *testing.T) {
	req := AnthropicMessagesRequest{
		Model:      "claude-opus-4",
		MaxTokens:  1024,
		Messages:   []AnthropicMessage{NewAnthropicTextMessage("user", "hi")},
		Tools:      []AnthropicToolDef{{Name: "Read", Description: "read file"}},
		ToolChoice: "auto",
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !json.Valid(b) {
		t.Fatal("invalid json")
	}
}
