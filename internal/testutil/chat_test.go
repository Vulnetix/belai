package testutil

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

type envelope struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role      string `json:"role"`
			Content   string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		Prompt     int `json:"prompt_tokens"`
		Completion int `json:"completion_tokens"`
		Total      int `json:"total_tokens"`
	} `json:"usage"`
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) envelope {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q", ct)
	}
	var e envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	if e.ID != "x" || e.Object != "chat.completion" || len(e.Choices) != 1 || e.Choices[0].Index != 0 {
		t.Fatalf("envelope = %+v", e)
	}
	return e
}

func TestWriteChat(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteChat(rec, "hello")
	e := decode(t, rec)
	c := e.Choices[0]
	if c.Message.Role != "assistant" || c.Message.Content != "hello" || c.FinishReason != "stop" || len(c.Message.ToolCalls) != 0 || e.Usage != nil {
		t.Fatalf("choice = %+v, usage %v", c, e.Usage)
	}
}

func TestWriteChatWithUsage(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteChatWithUsage(rec, "SAFE", 1, 2)
	e := decode(t, rec)
	if e.Usage == nil || e.Usage.Prompt != 1 || e.Usage.Completion != 2 || e.Usage.Total != 3 {
		t.Fatalf("usage = %+v", e.Usage)
	}
}

func TestWriteToolCall(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteToolCallWithContent(rec, "Read", `{"path":"a"}`, "reading")
	c := decode(t, rec).Choices[0]
	if c.FinishReason != "tool_calls" || c.Message.Content != "reading" || len(c.Message.ToolCalls) != 1 {
		t.Fatalf("choice = %+v", c)
	}
	tc := c.Message.ToolCalls[0]
	if tc.ID != "call_1" || tc.Type != "function" || tc.Function.Name != "Read" || tc.Function.Arguments != `{"path":"a"}` {
		t.Fatalf("tool call = %+v", tc)
	}
}
