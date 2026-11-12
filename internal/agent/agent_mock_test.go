package agent

import (
	"encoding/json"
	"io"
	"net/http"
)

// Shared responders for the package's fake OpenAI-compatible servers. Each
// writes one non-streaming chat.completion envelope.

// writeCompletion writes a one-choice chat.completion whose assistant message
// is msg and whose finish_reason is finish ("stop", "tool_calls", "length").
func writeCompletion(w http.ResponseWriter, msg map[string]any, finish string) {
	b, _ := json.Marshal(map[string]any{
		"id":     "x",
		"object": "chat.completion",
		"choices": []any{map[string]any{
			"index":         0,
			"message":       msg,
			"finish_reason": finish,
		}},
	})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

// toolCallMessage is an assistant message carrying one call_1 tool call.
func toolCallMessage(name, args, content string) map[string]any {
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

// writeChatJSON answers with plain assistant text.
func writeChatJSON(w http.ResponseWriter, content string) {
	writeCompletion(w, map[string]any{"role": "assistant", "content": content}, "stop")
}

// writeToolCallJSON answers with one tool call and no text.
func writeToolCallJSON(w http.ResponseWriter, name, args string) {
	writeToolCallWithContentJSON(w, name, args, "")
}

// writeToolCallWithContentJSON answers with one tool call alongside text.
func writeToolCallWithContentJSON(w http.ResponseWriter, name, args, content string) {
	writeCompletion(w, toolCallMessage(name, args, content), "tool_calls")
}

// writeLengthRepairJSON returns a tool call with finish_reason "length", which
// the loop treats as truncated and withholds — an unproductive iteration.
func writeLengthRepairJSON(w http.ResponseWriter, name, args string) {
	writeCompletion(w, toolCallMessage(name, args, ""), "length")
}

// chatMessage is the role and text of one request message.
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// decodeMessages reads the messages of a chat-completion request body. A
// body that does not decode yields none.
func decodeMessages(r io.Reader) []chatMessage {
	var req struct {
		Messages []chatMessage `json:"messages"`
	}
	_ = json.NewDecoder(r).Decode(&req)
	return req.Messages
}
