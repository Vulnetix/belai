package run

import (
	"encoding/json"
	"net/http"
)

// The package's fake OpenAI-compatible servers answer with the shared
// internal/testutil responders. This file keeps only the shape testutil does
// not write: a chat.completion under a chosen id.

// writeChatJSONWithID answers with text under the given id and an empty
// usage object, the shape the jev and provider-routing tests use.
func writeChatJSONWithID(w http.ResponseWriter, id, content string) {
	b, _ := json.Marshal(map[string]any{
		"id":     id,
		"object": "chat.completion",
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": content},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{},
	})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}
