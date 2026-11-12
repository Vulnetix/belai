package testutil

import (
	"encoding/json"
	"net/http"
	"strings"
)

// ChatRequest is an OpenAI chat-completion request as a fake model server
// reads it.
type ChatRequest struct {
	Model    string            `json:"model"`
	Stream   bool              `json:"stream"`
	Messages []ChatMessage     `json:"messages"`
	Tools    []json.RawMessage `json:"tools"`
}

// ChatMessage is one message of a ChatRequest. Content is the message text:
// a plain string, or the text parts of a content-part array joined by
// newlines. A null or missing content is empty.
type ChatMessage struct {
	Role       string
	Content    string
	ToolCallID string
	ToolCalls  []ChatToolCall
}

// ChatToolCall is one tool call an assistant message carries. Arguments is
// the raw JSON value the client sent (a JSON string on the OpenAI wire).
type ChatToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

// UnmarshalJSON reads a message whose content is a string, a content-part
// array or null.
func (m *ChatMessage) UnmarshalJSON(b []byte) error {
	var raw struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCallID string          `json:"tool_call_id"`
		ToolCalls  []ChatToolCall  `json:"tool_calls"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	m.Role, m.ToolCallID, m.ToolCalls = raw.Role, raw.ToolCallID, raw.ToolCalls
	m.Content = contentText(raw.Content)
	return nil
}

func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var texts []string
	for _, p := range parts {
		if p.Type == "text" {
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// DecodeChat reads an OpenAI chat-completion request body.
func DecodeChat(r *http.Request) (ChatRequest, error) {
	var req ChatRequest
	err := json.NewDecoder(r.Body).Decode(&req)
	return req, err
}

// ParseChat is DecodeChat for a body the server has already read.
func ParseChat(raw []byte) (ChatRequest, error) {
	var req ChatRequest
	err := json.Unmarshal(raw, &req)
	return req, err
}

// Text returns the last system message's content and every message's
// content, one per line.
func (c ChatRequest) Text() (system, all string) {
	var b strings.Builder
	for _, m := range c.Messages {
		if m.Role == "system" {
			system = m.Content
		}
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	return system, b.String()
}
