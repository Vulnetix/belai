package run

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/signet/internal/wire"
)

func TestStreamOpenAIChatDeltas(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatalf("expected flusher")
		}
		for _, word := range []string{"hello", " world"} {
			chunk, _ := json.Marshal(wire.OpenAIChatStreamChunk{
				Choices: []struct {
					Delta struct {
						Role    string `json:"role,omitempty"`
						Content string `json:"content,omitempty"`
					} `json:"delta"`
					FinishReason string `json:"finish_reason,omitempty"`
				}{{Delta: struct {
					Role    string `json:"role,omitempty"`
					Content string `json:"content,omitempty"`
				}{Content: word}}},
			})
			fmt.Fprintf(w, "data: %s\n\n", chunk)
			flusher.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	cfg := Config{Provider: "openai", BaseURL: srv.URL, APIKey: "sk", Model: "gpt-5"}
	ch, err := Stream(context.Background(), cfg, []Turn{{Role: "user", Content: "hi"}}, srv.Client())
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var out strings.Builder
	for c := range ch {
		if c.Err != nil {
			t.Fatalf("stream error: %v", c.Err)
		}
		if c.Done {
			break
		}
		out.WriteString(c.Text)
	}
	if out.String() != "hello world" {
		t.Fatalf("got %q", out.String())
	}
}

func TestStreamAnthropicDeltas(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		chunk, _ := json.Marshal(wire.AnthropicStreamEvent{Delta: struct {
			Type       string `json:"type,omitempty"`
			Text       string `json:"text,omitempty"`
			StopReason string `json:"stop_reason,omitempty"`
		}{Text: "anthropic-reply"}})
		fmt.Fprintf(w, "data: %s\n\n", chunk)
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	cfg := Config{Provider: "anthropic", BaseURL: srv.URL, APIKey: "sk", Model: "claude-opus-4"}
	ch, err := Stream(context.Background(), cfg, []Turn{{Role: "user", Content: "hi"}}, srv.Client())
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var out strings.Builder
	for c := range ch {
		if c.Err != nil {
			t.Fatalf("stream error: %v", c.Err)
		}
		if c.Done {
			break
		}
		out.WriteString(c.Text)
	}
	if out.String() != "anthropic-reply" {
		t.Fatalf("got %q", out.String())
	}
}

func TestStreamWorkersAI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		chunk, _ := json.Marshal(wire.OpenAIChatStreamChunk{
			Choices: []struct {
				Delta struct {
					Role    string `json:"role,omitempty"`
					Content string `json:"content,omitempty"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason,omitempty"`
			}{{Delta: struct {
				Role    string `json:"role,omitempty"`
				Content string `json:"content,omitempty"`
			}{Content: "workers-reply"}}},
		})
		fmt.Fprintf(w, "data: %s\n\n", chunk)
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	cfg := Config{Provider: "cloudflare-workers-ai", BaseURL: srv.URL, APIKey: "sk", Model: "@cf/moonshotai/kimi-k2.6"}
	ch, err := Stream(context.Background(), cfg, []Turn{{Role: "user", Content: "hi"}}, srv.Client())
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var out strings.Builder
	for c := range ch {
		if c.Err != nil {
			t.Fatalf("stream error: %v", c.Err)
		}
		if c.Done {
			break
		}
		out.WriteString(c.Text)
	}
	if out.String() != "workers-reply" {
		t.Fatalf("got %q", out.String())
	}
}

func TestStreamCancelStopsEarly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for i := 0; i < 10; i++ {
			fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"content":"x"}}]}`)
			flusher.Flush()
			time.Sleep(50 * time.Millisecond)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cfg := Config{Provider: "openai", BaseURL: srv.URL, APIKey: "sk", Model: "gpt-5"}
	ch, err := Stream(ctx, cfg, []Turn{{Role: "user", Content: "hi"}}, srv.Client())
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	go func() {
		time.Sleep(80 * time.Millisecond)
		cancel()
	}()
	var closed bool
	for c := range ch {
		if c.Err != nil {
			break
		}
		if c.Done {
			closed = true
			break
		}
	}
	if closed {
		t.Fatalf("expected cancellation, not normal close")
	}
}

func TestStreamErrorStatusRedactsKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		fmt.Fprint(w, `error: sk-secret is invalid`)
	}))
	defer srv.Close()

	cfg := Config{Provider: "openai", BaseURL: srv.URL, APIKey: "sk-secret", Model: "gpt-5"}
	_, err := Stream(context.Background(), cfg, []Turn{{Role: "user", Content: "hi"}}, srv.Client())
	if err == nil {
		t.Fatalf("expected error")
	}
	if strings.Contains(err.Error(), "sk-secret") {
		t.Fatalf("error leaked key: %v", err)
	}
}

func TestStreamAndRunTurnsSealIdentically(t *testing.T) {
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, b)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()

	cfg := Config{Provider: "openai", BaseURL: srv.URL, APIKey: "sk", Model: "gpt-5"}
	RunTurns(cfg, []Turn{{Role: "user", Content: "ping"}}, srv.Client())

	// Stream sends accept: text/event-stream so let it hit the same handler.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, b)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv2.Close()
	cfg2 := Config{Provider: "openai", BaseURL: srv2.URL, APIKey: "sk", Model: "gpt-5"}
	ch, _ := Stream(context.Background(), cfg2, []Turn{{Role: "user", Content: "ping"}}, srv2.Client())
	for range ch {
	}

	if len(bodies) != 2 {
		t.Fatalf("expected 2 bodies, got %d", len(bodies))
	}
	var r1, r2 wire.OpenAIChatRequest
	json.Unmarshal(bodies[0], &r1)
	json.Unmarshal(bodies[1], &r2)
	if len(r1.Messages) != len(r2.Messages) {
		t.Fatalf("message count differs: %d vs %d", len(r1.Messages), len(r2.Messages))
	}
	for i := range r1.Messages {
		if r1.Messages[i].Role != r2.Messages[i].Role {
			t.Fatalf("message %d role differs", i)
		}
		c1 := stripNonce(r1.Messages[i].Content)
		c2 := stripNonce(r2.Messages[i].Content)
		if c1 != c2 {
			t.Fatalf("message %d content differs: %q vs %q", i, c1, c2)
		}
	}
}
