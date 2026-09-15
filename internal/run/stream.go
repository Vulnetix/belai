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
	"github.com/vulnetix/signet/internal/wire"
)

// Chunk is one piece of a streamed response.
type Chunk struct {
	Text string
	Err  error
	Done bool
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

	verifiedSystem, err := SealSystem(pool, opts)
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

	req, err := buildRequest(cfg, verifiedSystem, sanitized, true)
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
		for scan.Scan() {
			line := scan.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				ch <- Chunk{Done: true}
				return
			}
			text, err := decodeDelta(cfg, data)
			if err != nil {
				ch <- Chunk{Err: err}
				return
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
		ch <- Chunk{Done: true}
	}()
	return ch, nil
}

func decodeDelta(cfg Config, data string) (string, error) {
	switch cfg.Provider {
	case "anthropic":
		var ev wire.AnthropicStreamEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return "", err
		}
		return ev.Delta.Text, nil
	case "cloudflare-ai-gateway":
		// Gateway dispatches by model prefix.
		if strings.HasPrefix(strings.ToLower(cfg.Model), "claude") {
			var ev wire.AnthropicStreamEvent
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				return "", err
			}
			return ev.Delta.Text, nil
		}
		var chunk wire.OpenAIChatStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return "", err
		}
		if len(chunk.Choices) > 0 {
			return chunk.Choices[0].Delta.Content, nil
		}
		return "", nil
	case "cloudflare-workers-ai":
		var chunk wire.OpenAIChatStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return "", err
		}
		if len(chunk.Choices) > 0 {
			return chunk.Choices[0].Delta.Content, nil
		}
		return "", nil
	default:
		var chunk wire.OpenAIChatStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return "", err
		}
		if len(chunk.Choices) > 0 {
			return chunk.Choices[0].Delta.Content, nil
		}
		return "", nil
	}
}
