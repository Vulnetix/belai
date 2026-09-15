// Package run implements the noninteractive prompt path: resolve a provider
// from environment variables the same way Pi Coding Agent does, run the Role
// Manager pipeline over the user prompt, and return the completion text.
package run

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/vulnetix/signet/internal/delimiters"
	"github.com/vulnetix/signet/internal/nonce"
	"github.com/vulnetix/signet/internal/prompt"
	"github.com/vulnetix/signet/internal/sanitize"
	"github.com/vulnetix/signet/internal/transcript"
	"github.com/vulnetix/signet/internal/wire"
)

// Chunk is one piece of a streamed response.
type Chunk struct {
	Text  string
	Err   error
	Done  bool
	Usage *transcript.Usage // set on the Done chunk when the provider reported it
}

// Stream sends a conversation and returns a channel of text deltas.
// Cancellation runs through ctx. The channel is always closed exactly once.
func Stream(ctx context.Context, cfg Config, turns []Turn, client *http.Client) (<-chan Chunk, error) {
	return StreamWithPool(ctx, cfg, turns, client, nonce.New(), prompt.Options{})
}

// StreamWithPool is Stream with a caller-provided nonce pool.
func StreamWithPool(ctx context.Context, cfg Config, turns []Turn, client *http.Client, pool *nonce.Pool, opts prompt.Options) (<-chan Chunk, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if pool == nil {
		pool = nonce.New()
	}

	verifiedSystem, err := SealSystem(cfg, pool, opts)
	if err != nil {
		return nil, err
	}

	sanitized := make([]Turn, len(turns))
	for i, t := range turns {
		sanitized[i] = Turn{
			Role:    t.Role,
			Content: delimiters.Egress(sanitize.Sanitize(t.Content), pool),
		}
	}

	req, err := buildRequest(cfg, verifiedSystem, sanitized, true, nil, nil)
	if err != nil {
		return nil, err
	}
	req = req.WithContext(ctx)
	req.Header.Set("accept", "text/event-stream")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		msg := strings.TrimSpace(string(body))
		msg = strings.ReplaceAll(msg, cfg.APIKey, "<redacted>")
		return nil, fmt.Errorf("provider returned %d: %s", resp.StatusCode, msg)
	}

	ch := make(chan Chunk)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		scan := bufio.NewScanner(resp.Body)
		scan.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		var acc transcript.Usage
		sendDone := func() {
			u := acc
			ch <- Chunk{Done: true, Usage: &u}
		}
		for scan.Scan() {
			line := scan.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				sendDone()
				return
			}
			text, usage, err := decodeDelta(cfg, data)
			if err != nil {
				ch <- Chunk{Err: err}
				return
			}
			if usage != nil {
				acc.PromptTokens += usage.PromptTokens
				acc.CompletionTokens += usage.CompletionTokens
				if usage.TotalTokens != 0 {
					acc.TotalTokens = usage.TotalTokens
				}
			}
			if text != "" {
				ch <- Chunk{Text: text}
			}
			select {
			case <-ctx.Done():
				return
			default:
			}
		}
		if err := scan.Err(); err != nil {
			ch <- Chunk{Err: fmt.Errorf("stream read: %w", err)}
		}
		sendDone()
	}()
	return ch, nil
}

// decodeDelta returns the text delta and any usage carried by this SSE
// payload. A payload may carry usage with no text (OpenAI's final usage chunk,
// Anthropic's message_start).
func decodeDelta(cfg Config, data string) (string, *transcript.Usage, error) {
	switch cfg.Provider {
	case "anthropic":
		var ev wire.AnthropicStreamEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return "", nil, err
		}
		return ev.Delta.Text, anthropicEventUsage(&ev), nil
	case "cloudflare-ai-gateway":
		// Gateway dispatches by model prefix.
		if strings.HasPrefix(strings.ToLower(cfg.Model), "claude") {
			var ev wire.AnthropicStreamEvent
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				return "", nil, err
			}
			return ev.Delta.Text, anthropicEventUsage(&ev), nil
		}
		var chunk wire.OpenAIChatStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return "", nil, err
		}
		return openAIDelta(&chunk), openAIChunkUsage(&chunk), nil
	case "cloudflare-workers-ai":
		var chunk wire.OpenAIChatStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return "", nil, err
		}
		return openAIDelta(&chunk), openAIChunkUsage(&chunk), nil
	default:
		var chunk wire.OpenAIChatStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return "", nil, err
		}
		return openAIDelta(&chunk), openAIChunkUsage(&chunk), nil
	}
}

func openAIDelta(chunk *wire.OpenAIChatStreamChunk) string {
	if len(chunk.Choices) > 0 {
		return chunk.Choices[0].Delta.Content
	}
	return ""
}

func openAIChunkUsage(chunk *wire.OpenAIChatStreamChunk) *transcript.Usage {
	if chunk.Usage == nil || chunk.Usage.TotalTokens <= 0 {
		return nil
	}
	return &transcript.Usage{
		PromptTokens:     chunk.Usage.PromptTokens,
		CompletionTokens: chunk.Usage.CompletionTokens,
		TotalTokens:      chunk.Usage.TotalTokens,
	}
}

func anthropicEventUsage(ev *wire.AnthropicStreamEvent) *transcript.Usage {
	var u wire.AnthropicUsage
	if ev.Message != nil && ev.Message.Usage != nil {
		u = *ev.Message.Usage
	}
	if ev.Usage != nil {
		if ev.Usage.InputTokens != 0 {
			u.InputTokens = ev.Usage.InputTokens
		}
		if ev.Usage.OutputTokens != 0 {
			u.OutputTokens = ev.Usage.OutputTokens
		}
		if ev.Usage.CacheCreationInputTokens != 0 {
			u.CacheCreationInputTokens = ev.Usage.CacheCreationInputTokens
		}
		if ev.Usage.CacheReadInputTokens != 0 {
			u.CacheReadInputTokens = ev.Usage.CacheReadInputTokens
		}
	}
	if u.InputTokens == 0 && u.OutputTokens == 0 {
		return nil
	}
	return &transcript.Usage{
		PromptTokens:     u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens,
		CompletionTokens: u.OutputTokens,
	}
}
