package agent

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/vulnetix/belai/internal/testutil"
)

// Agent-specific pieces of the package's fake OpenAI-compatible servers. The
// common chat.completion responders (plain text, one tool call) live in
// internal/testutil.

// writeLengthRepairJSON returns a tool call with finish_reason "length", which
// the loop treats as truncated and withholds — an unproductive iteration.
func writeLengthRepairJSON(w http.ResponseWriter, name, args string) {
	testutil.WriteCompletion(w, testutil.ToolCallMessage(name, args, ""), "length")
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
