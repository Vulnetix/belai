package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/signet/internal/delimiters"
	"github.com/vulnetix/signet/internal/nonce"
	"github.com/vulnetix/signet/internal/provider"
	"github.com/vulnetix/signet/internal/resilience"
	"github.com/vulnetix/signet/internal/transcript"
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
			chunk, _ := json.Marshal(map[string]any{
				"choices": []any{map[string]any{
					"delta": map[string]any{"content": word},
				}},
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
		chunk, _ := json.Marshal(map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "text_delta", "text": "anthropic-reply"}})
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
	t.Run("native response shape", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			fmt.Fprint(w, "data: {\"response\":\"workers-reply\"}\n\n")
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
	})

	t.Run("openai shaped gateway variant", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			chunk, _ := json.Marshal(map[string]any{
				"choices": []any{map[string]any{
					"delta": map[string]any{"content": "gateway-reply"},
				}},
			})
			fmt.Fprintf(w, "data: %s\n\n", chunk)
			fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
		}))
		defer srv.Close()

		cfg := Config{Provider: "cloudflare-workers-ai", BaseURL: srv.URL, APIKey: "sk", Model: "@cf/meta/llama-4"}
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
		if out.String() != "gateway-reply" {
			t.Fatalf("got %q", out.String())
		}
	})

	t.Run("tool calls complete on done", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			fmt.Fprint(w, "data: {\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"Read\",\"arguments\":\"{\\\"path\\\":\\\"a.go\\\"}\"}}]}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
		}))
		defer srv.Close()

		cfg := Config{Provider: "cloudflare-workers-ai", BaseURL: srv.URL, APIKey: "sk", Model: "@cf/deepseek-ai/deepseek-v4-pro-0813"}
		ch, err := Stream(context.Background(), cfg, []Turn{{Role: "user", Content: "hi"}}, srv.Client())
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		var done *Assistant
		for c := range ch {
			if c.Err != nil {
				t.Fatalf("stream error: %v", c.Err)
			}
			if c.Done {
				done = c.Assistant
				break
			}
		}
		if done == nil || len(done.ToolCalls) != 1 {
			t.Fatalf("expected one completed tool call, got %+v", done)
		}
		if done.ToolCalls[0].ID != "call_1" || done.ToolCalls[0].Name != "Read" || done.ToolCalls[0].RawArgs != `{"path":"a.go"}` {
			t.Fatalf("tool call = %+v", done.ToolCalls[0])
		}
	})
}

func TestStreamWorkersAIReasoning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"reasoning_content\":\"thinking through the request\"}\n\n")
		fmt.Fprint(w, "data: {\"response\":\"answer\"}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	cfg := Config{Provider: "cloudflare-workers-ai", BaseURL: srv.URL, APIKey: "sk", Model: "@cf/deepseek-ai/deepseek-v4-pro-0813"}
	ch, err := Stream(context.Background(), cfg, []Turn{{Role: "user", Content: "hi"}}, srv.Client())
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var reasoning strings.Builder
	var done *Assistant
	for c := range ch {
		if c.Err != nil {
			t.Fatalf("stream error: %v", c.Err)
		}
		reasoning.WriteString(c.Reasoning)
		if c.Done {
			done = c.Assistant
			break
		}
	}
	if reasoning.String() != "thinking through the request" {
		t.Fatalf("reasoning chunks = %q", reasoning.String())
	}
	if done == nil || done.Reasoning != "thinking through the request" {
		t.Fatalf("done assistant reasoning = %+v", done)
	}
}

func TestStreamOpenAIReasoningContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"step one\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	cfg := Config{Provider: "openai", BaseURL: srv.URL, APIKey: "sk", Model: "gpt-5"}
	ch, err := Stream(context.Background(), cfg, []Turn{{Role: "user", Content: "hi"}}, srv.Client())
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var reasoning strings.Builder
	var done *Assistant
	for c := range ch {
		if c.Err != nil {
			t.Fatalf("stream error: %v", c.Err)
		}
		reasoning.WriteString(c.Reasoning)
		if c.Done {
			done = c.Assistant
			break
		}
	}
	if reasoning.String() != "step one" || done == nil || done.Reasoning != "step one" {
		t.Fatalf("reasoning = %q, done = %+v", reasoning.String(), done)
	}
}

func TestStreamAnthropicThinking(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"pondering\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	cfg := Config{Provider: "anthropic", BaseURL: srv.URL, APIKey: "sk", Model: "claude-opus-4"}
	ch, err := Stream(context.Background(), cfg, []Turn{{Role: "user", Content: "hi"}}, srv.Client())
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var reasoning strings.Builder
	var done *Assistant
	for c := range ch {
		if c.Err != nil {
			t.Fatalf("stream error: %v", c.Err)
		}
		reasoning.WriteString(c.Reasoning)
		if c.Done {
			done = c.Assistant
			break
		}
	}
	if reasoning.String() != "pondering" || done == nil || done.Reasoning != "pondering" {
		t.Fatalf("reasoning = %q, done = %+v", reasoning.String(), done)
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
	RunTurns(context.Background(), cfg, []Turn{{Role: "user", Content: "ping"}}, srv.Client())

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

func TestStreamOpenAIUsageOnDoneChunk(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"pong\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":20,\"total_tokens\":30}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	cfg := Config{Provider: "openai", BaseURL: srv.URL, APIKey: "sk", Model: "gpt-5"}
	ch, err := Stream(context.Background(), cfg, []Turn{{Role: "user", Content: "hi"}}, srv.Client())
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var usage *transcript.Usage
	for c := range ch {
		if c.Err != nil {
			t.Fatalf("stream error: %v", c.Err)
		}
		if c.Done {
			usage = c.Usage
			break
		}
	}
	if usage == nil || usage.Total() != 30 {
		t.Fatalf("Done usage = %+v, want total 30", usage)
	}
}

func TestStreamAnthropicUsageMerged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":100,\"cache_read_input_tokens\":5}}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":7}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	cfg := Config{Provider: "anthropic", BaseURL: srv.URL, APIKey: "sk", Model: "claude-opus-4-5"}
	ch, err := Stream(context.Background(), cfg, []Turn{{Role: "user", Content: "hi"}}, srv.Client())
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var usage *transcript.Usage
	for c := range ch {
		if c.Err != nil {
			t.Fatalf("stream error: %v", c.Err)
		}
		if c.Done {
			usage = c.Usage
			break
		}
	}
	if usage == nil {
		t.Fatalf("expected usage on Done chunk")
	}
	if usage.PromptTokens != 105 || usage.CompletionTokens != 7 || usage.Total() != 112 {
		t.Fatalf("merged usage = %+v, want prompt 105 completion 7 total 112", usage)
	}
}

func TestStreamNoUsageIsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"pong\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	cfg := Config{Provider: "openai", BaseURL: srv.URL, APIKey: "sk", Model: "gpt-5"}
	ch, err := Stream(context.Background(), cfg, []Turn{{Role: "user", Content: "hi"}}, srv.Client())
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var usage *transcript.Usage
	var sawDone bool
	for c := range ch {
		if c.Done {
			usage = c.Usage
			sawDone = true
			break
		}
	}
	if !sawDone || usage != nil {
		t.Fatalf("no-usage stream must end with nil usage, got %+v (done=%v)", usage, sawDone)
	}
}

func TestStreamGatewayClaudeDecodesAnthropicEvents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		chunk, _ := json.Marshal(map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "text_delta", "text": "gateway-claude-reply"}})
		fmt.Fprintf(w, "data: %s\n\n", chunk)
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	cfg := Config{Provider: "cloudflare-ai-gateway", BaseURL: srv.URL, APIKey: "sk", Model: "claude-sonnet-4-5"}
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
	if out.String() != "gateway-claude-reply" {
		t.Fatalf("got %q", out.String())
	}
}

func TestEgressTurnsSealsAttachments(t *testing.T) {
	pool := nonce.New()
	if err := pool.Seed(4); err != nil {
		t.Fatal(err)
	}
	turns := []Turn{{
		Role:    "user",
		Content: "explain this file",
		Attachments: []Attachment{
			{Kind: "file", Label: "README.md", Body: "hello world"},
		},
	}}
	out := egressTurns(turns, pool)
	body := out[0].Content
	if !strings.Contains(body, "<attachment ") {
		t.Fatalf("expected sealed attachment block in %q", body)
	}
	// A second Egress pass with the same pool must keep the block.
	again := delimiters.Egress(body, pool)
	if again != body {
		t.Fatalf("second egress changed content")
	}
	// A fresh pool (no valid nonces) strips it.
	fresh := nonce.New()
	stripped := delimiters.Egress(body, fresh)
	if strings.Contains(stripped, "<attachment ") {
		t.Fatalf("expected attachment stripped by fresh pool")
	}
}

func TestStreamCustomAnthropicDialectDecodesEvents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		chunk, _ := json.Marshal(map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "text_delta", "text": "custom-anthropic-reply"}})
		fmt.Fprintf(w, "data: %s\n\n", chunk)
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	cfg := Config{Provider: "my-llm", BaseURL: srv.URL, APIKey: "sk", Model: "m1", API: wire.SurfaceAnthropicMessages, Auth: provider.AuthXAPIKey}
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
	if out.String() != "custom-anthropic-reply" {
		t.Fatalf("got %q", out.String())
	}
}

func TestStreamDoneAlwaysBuildsAssistant(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	cfg := Config{Provider: "openai", BaseURL: srv.URL, APIKey: "sk", Model: "gpt-5"}
	ch, err := Stream(context.Background(), cfg, []Turn{{Role: "user", Content: "hi"}}, srv.Client())
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var asst *Assistant
	for c := range ch {
		if c.Done {
			asst = c.Assistant
			break
		}
	}
	if asst == nil {
		t.Fatalf("expected Assistant on Done chunk")
	}
}

func TestStreamRetriesOpenStream(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`temporarily unavailable`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	policy := resilience.Policy{MaxAttempts: 3, Base: time.Millisecond, Cap: time.Millisecond, Jitter: 0}
	cfg := Config{Provider: "openai", BaseURL: srv.URL, APIKey: "sk", Model: "gpt-5"}
	ch, err := Stream(context.Background(), cfg, []Turn{{Role: "user", Content: "hi"}}, srv.Client())
	_ = policy
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var sawDone bool
	for c := range ch {
		if c.Done {
			sawDone = true
			break
		}
	}
	if !sawDone {
		t.Fatal("did not see done")
	}
	if calls < 2 {
		t.Fatalf("expected retry, calls=%d", calls)
	}
}

// h2FlakeTransport fails its first round trip with the HTTP/2 stream reset a
// gateway peer sends, then serves a completed SSE stream.
type h2FlakeTransport struct {
	calls     int
	idleDrops int
}

func (f *h2FlakeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.calls++
	if f.calls == 1 {
		return nil, errors.New("stream error: stream ID 85; PROTOCOL_ERROR; received from peer")
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")),
		Request:    r,
	}, nil
}

func (f *h2FlakeTransport) CloseIdleConnections() { f.idleDrops++ }

// TestStreamRetriesHTTP2StreamError pins the gateway PROTOCOL_ERROR fix: a
// peer stream reset before the first byte is retried on a fresh connection
// instead of failing the turn.
func TestStreamRetriesHTTP2StreamError(t *testing.T) {
	tr := &h2FlakeTransport{}
	client := &http.Client{Transport: tr}
	cfg := Config{Provider: "openai", BaseURL: "https://gateway.example/v1", APIKey: "sk", Model: "gpt-5"}
	var retries []resilience.Attempt
	ch, err := StreamTurnsWithTools(context.Background(), cfg, "", []Turn{{Role: "user", Content: "hi"}}, client, nil, nil, nil,
		func(a resilience.Attempt) { retries = append(retries, a) })
	if err != nil {
		t.Fatalf("StreamTurnsWithTools: %v", err)
	}
	var out strings.Builder
	for c := range ch {
		if c.Err != nil {
			t.Fatalf("stream error: %v", c.Err)
		}
		out.WriteString(c.Text)
		if c.Done {
			break
		}
	}
	if out.String() != "ok" {
		t.Fatalf("got %q, want ok", out.String())
	}
	if tr.calls != 2 || len(retries) != 1 {
		t.Fatalf("calls=%d retries=%d, want 2 and 1", tr.calls, len(retries))
	}
	if retries[0].Max != defaultRetryPolicy.MaxAttempts {
		t.Fatalf("retry max = %d, want %d", retries[0].Max, defaultRetryPolicy.MaxAttempts)
	}
	if tr.idleDrops == 0 {
		t.Fatal("transport failure did not drop idle connections before the retry")
	}
}

func TestIdleWatchdogTearsDownAfterGap(t *testing.T) {
	pr, pw := io.Pipe()
	wd := newIdleWatchdog(pr, 20*time.Millisecond)
	wd.start(func() { pw.CloseWithError(errors.New("closed by idle watchdog")) })
	defer wd.stop()

	// Feed one byte to reset the timer, then leave the pipe idle.
	go func() { _, _ = pw.Write([]byte("x")) }()
	buf := make([]byte, 1)
	if _, err := wd.Read(buf); err != nil {
		t.Fatalf("first read: %v", err)
	}

	// The next read blocks; the watchdog fires after the gap and closes the
	// pipe, unblocking the read with an error.
	if _, err := wd.Read(buf); err == nil {
		t.Fatal("expected an error after the idle gap")
	}
	if !wd.fired() {
		t.Fatal("watchdog should have fired")
	}
}

func TestEgressTurnsMemoisedAcrossCalls(t *testing.T) {
	pool := nonce.New()
	if err := pool.Seed(16); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	turns := []Turn{
		{Role: "user", Content: "hello", Attachments: []Attachment{{Kind: "file", Label: "@f", Body: "file body"}}},
	}

	first := egressTurns(turns, pool)
	if first[0].Content == turns[0].Content {
		t.Fatalf("first egress did not seal/egress content: %q", first[0].Content)
	}
	if turns[0].egrossed == "" {
		t.Fatal("memo not written back to the input turn")
	}
	activeAfterFirst := pool.Active()

	second := egressTurns(turns, pool)
	if second[0].Content != first[0].Content {
		t.Fatalf("memoised egress differs:\n%q\n---\n%q", first[0].Content, second[0].Content)
	}
	if pool.Active() != activeAfterFirst {
		t.Fatalf("memoised egress reserved new nonces: active %d -> %d", activeAfterFirst, pool.Active())
	}
}

// TestStreamDrainsToolCallsOnNonToolFinish pins the streaming-drop fix: an
// OpenAI-shaped stream that closes with stop/length (or a bare [DONE]) must
// still surface its accumulated tool calls, not silently drop them.
func TestStreamDrainsToolCallsOnNonToolFinish(t *testing.T) {
	cases := []struct {
		name        string
		finishChunk string // "" means no finish chunk at all
	}{
		{"stop", `data: {"choices":[{"finish_reason":"stop"}]}` + "\n\n"},
		{"length", `data: {"choices":[{"finish_reason":"length"}]}` + "\n\n"},
		{"none", ""},
		{"tool_calls", `data: {"choices":[{"finish_reason":"tool_calls"}]}` + "\n\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				flusher, _ := w.(http.Flusher)
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"Read\",\"arguments\":\"{\\\"path\\\":\\\"\"}}]}}]}\n\n")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"a.go\\\"}\"}}]}}]}\n\n")
				if tc.finishChunk != "" {
					fmt.Fprint(w, tc.finishChunk)
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
			var done *Assistant
			for c := range ch {
				if c.Err != nil {
					t.Fatalf("stream error: %v", c.Err)
				}
				if c.Done {
					done = c.Assistant
					break
				}
			}
			if done == nil || len(done.ToolCalls) != 1 {
				t.Fatalf("expected exactly one tool call, got %+v", done)
			}
			if done.ToolCalls[0].ID != "call_1" || done.ToolCalls[0].Name != "Read" || done.ToolCalls[0].RawArgs != `{"path":"a.go"}` {
				t.Fatalf("tool call = %+v", done.ToolCalls[0])
			}
		})
	}
}

// TestStreamAnthropicToolCallCountUnchanged pins the regression guard for the
// Anthropic shape: content_block_stop already completes the call, so the
// unconditional [DONE]-drain must not double-count it.
func TestStreamAnthropicToolCallCountUnchanged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"Read\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\\\"\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"a.go\\\"}\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	cfg := Config{Provider: "anthropic", BaseURL: srv.URL, APIKey: "sk", Model: "claude-opus-4"}
	ch, err := Stream(context.Background(), cfg, []Turn{{Role: "user", Content: "hi"}}, srv.Client())
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var done *Assistant
	for c := range ch {
		if c.Err != nil {
			t.Fatalf("stream error: %v", c.Err)
		}
		if c.Done {
			done = c.Assistant
			break
		}
	}
	if done == nil || len(done.ToolCalls) != 1 {
		t.Fatalf("expected exactly one tool call, got %+v", done)
	}
	if done.ToolCalls[0].RawArgs != `{"path":"a.go"}` {
		t.Fatalf("tool call = %+v", done.ToolCalls[0])
	}
}
