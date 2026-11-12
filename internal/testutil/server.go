package testutil

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// ChatServer is a fake OpenAI chat-completion endpoint. Every POST is decoded
// into a ChatRequest, recorded, and answered with the content its reply
// function returns: a chat.completion envelope (WriteChat), or a one-chunk
// server-sent-event stream when the request sets stream. Any other method
// gets 404. The server closes when the test ends.
type ChatServer struct {
	*httptest.Server

	mu       sync.Mutex
	requests []ChatRequest
}

// NewChatServer starts a ChatServer answering each request with reply(req).
// reply runs on the server's handler goroutine and may be called
// concurrently; use Reply for a fixed answer.
func NewChatServer(t testing.TB, reply func(ChatRequest) string) *ChatServer {
	t.Helper()
	s := &ChatServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		req, err := DecodeChat(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.requests = append(s.requests, req)
		s.mu.Unlock()
		content := reply(req)
		if req.Stream {
			WriteChatStream(w, content)
			return
		}
		WriteChat(w, content)
	}))
	t.Cleanup(s.Close)
	return s
}

// Reply is a reply function that always answers content.
func Reply(content string) func(ChatRequest) string {
	return func(ChatRequest) string { return content }
}

// Requests returns a copy of the requests the server has decoded, oldest
// first.
func (s *ChatServer) Requests() []ChatRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ChatRequest(nil), s.requests...)
}

// WriteChatStream answers with content as one server-sent-event delta chunk
// followed by [DONE].
func WriteChatStream(w http.ResponseWriter, content string) {
	chunk, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{"content": content}}},
	})
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, "data: %s\n\n", chunk)
	fmt.Fprint(w, "data: [DONE]\n\n")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}
