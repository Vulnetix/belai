// Package testutil holds fakes shared by tests in several packages. Its chat
// responders write one non-streaming OpenAI-compatible chat.completion
// envelope, the reply most fake model servers in the tests send.
//
// Only test files import this package.
package testutil

import (
	"encoding/json"
	"net/http"
)

// WriteCompletion writes a one-choice chat.completion (id "x") whose
// assistant message is msg and whose finish_reason is finish.
func WriteCompletion(w http.ResponseWriter, msg map[string]any, finish string) {
	writeEnvelope(w, msg, finish, nil)
}

// WriteChat answers with plain assistant text and finish_reason "stop".
func WriteChat(w http.ResponseWriter, content string) {
	WriteCompletion(w, TextMessage(content), "stop")
}

// WriteChatWithUsage answers like WriteChat and reports token usage.
func WriteChatWithUsage(w http.ResponseWriter, content string, promptTokens, completionTokens int) {
	writeEnvelope(w, TextMessage(content), "stop", map[string]any{
		"prompt_tokens":     promptTokens,
		"completion_tokens": completionTokens,
		"total_tokens":      promptTokens + completionTokens,
	})
}

// WriteToolCall answers with one tool call (id "call_1") and no text, with
// finish_reason "tool_calls".
func WriteToolCall(w http.ResponseWriter, name, args string) {
	WriteToolCallWithContent(w, name, args, "")
}

// WriteToolCallWithContent answers with one tool call alongside text.
func WriteToolCallWithContent(w http.ResponseWriter, name, args, content string) {
	WriteCompletion(w, ToolCallMessage(name, args, content), "tool_calls")
}

// TextMessage is an assistant message carrying content.
func TextMessage(content string) map[string]any {
	return map[string]any{"role": "assistant", "content": content}
}

// ToolCallMessage is an assistant message carrying content and one call_1
// function call to name with the raw JSON args.
func ToolCallMessage(name, args, content string) map[string]any {
	return map[string]any{
		"role":    "assistant",
		"content": content,
		"tool_calls": []any{map[string]any{
			"id":       "call_1",
			"type":     "function",
			"function": map[string]any{"name": name, "arguments": args},
		}},
	}
}

func writeEnvelope(w http.ResponseWriter, msg map[string]any, finish string, usage map[string]any) {
	body := map[string]any{
		"id":     "x",
		"object": "chat.completion",
		"choices": []any{map[string]any{
			"index":         0,
			"message":       msg,
			"finish_reason": finish,
		}},
	}
	if usage != nil {
		body["usage"] = usage
	}
	b, _ := json.Marshal(body)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}
