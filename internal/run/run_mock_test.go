package run

import (
	"encoding/json"
	"io"
	"net/http"
)

// Shared responders for the package's fake OpenAI-compatible servers. Each
// writes one non-streaming chat.completion with a single "stop" choice.

type mockMessage struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	Reasoning string `json:"reasoning_content,omitempty"`
}

// writeChat writes the envelope with the given id and assistant message.
// usage, when set, is raw JSON for a top-level "usage" object.
func writeChat(w http.ResponseWriter, id string, msg mockMessage, usage string) {
	idJSON, _ := json.Marshal(id)
	msgJSON, _ := json.Marshal(msg)
	body := `{"id":` + string(idJSON) + `,"object":"chat.completion","choices":[{"index":0,"message":` +
		string(msgJSON) + `,"finish_reason":"stop"}]`
	if usage != "" {
		body += `,"usage":` + usage
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, body+"}")
}

// writeChatJSON answers with plain assistant text under id "x".
func writeChatJSON(w http.ResponseWriter, content string) {
	writeChat(w, "x", mockMessage{Role: "assistant", Content: content}, "")
}

// writeChatJSONReasoning answers with text plus a reasoning_content field.
func writeChatJSONReasoning(w http.ResponseWriter, content, reasoning string) {
	writeChat(w, "x", mockMessage{Role: "assistant", Content: content, Reasoning: reasoning}, "")
}

// writeChatJSONUsage answers with text and the given raw usage object.
func writeChatJSONUsage(w http.ResponseWriter, content, usage string) {
	writeChat(w, "x", mockMessage{Role: "assistant", Content: content}, usage)
}

// writeChatJSONWithID answers with text under the given id and an empty
// usage object, the shape the jev and provider-routing tests use.
func writeChatJSONWithID(w http.ResponseWriter, id, content string) {
	writeChat(w, id, mockMessage{Role: "assistant", Content: content}, "{}")
}
