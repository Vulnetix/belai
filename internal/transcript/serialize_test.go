package transcript

import (
	"strings"
	"testing"
)

func TestSerializeBasic(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
	}
	s := Serialize(msgs, SerializeOptions{})
	if !strings.HasPrefix(s, "<conversation>") {
		t.Fatalf("expected <conversation> prefix, got %q", s)
	}
	if !strings.HasSuffix(s, "</conversation>") {
		t.Fatalf("expected </conversation> suffix, got %q", s)
	}
	if !strings.Contains(s, "[user]") {
		t.Fatal("missing user label")
	}
	if !strings.Contains(s, "[assistant]") {
		t.Fatal("missing assistant label")
	}
}

func TestSerializeDropsSystem(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "hi"},
	}
	s := Serialize(msgs, SerializeOptions{})
	if strings.Contains(s, "system prompt") {
		t.Fatal("system content should be dropped")
	}
}

func TestSerializeToolName(t *testing.T) {
	msgs := []Message{
		{Role: "tool", Content: "result", ToolName: "Read"},
	}
	s := Serialize(msgs, SerializeOptions{})
	if !strings.Contains(s, "[tool: Read]") {
		t.Fatalf("expected tool label, got %q", s)
	}
}

func TestSerializeTruncatesToolResult(t *testing.T) {
	long := strings.Repeat("a", DefaultMaxToolResultChars+100)
	msgs := []Message{
		{Role: "tool", Content: long},
	}
	s := Serialize(msgs, SerializeOptions{MaxToolResultChars: 50})
	if strings.Contains(s, strings.Repeat("a", 60)) {
		t.Fatal("tool result should be truncated")
	}
	if !strings.Contains(s, "truncated") {
		t.Fatal("truncation marker missing")
	}
}

func TestSerializeWithNonce(t *testing.T) {
	msgs := []Message{{Role: "user", Content: "hi"}}
	s := Serialize(msgs, SerializeOptions{Nonce: "abc123"})
	if !strings.Contains(s, `<conversation id="abc123">`) {
		t.Fatalf("expected nonce in open tag, got %q", s)
	}
}
