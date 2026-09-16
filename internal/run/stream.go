// Package run implements the noninteractive prompt path: resolve a provider
// from environment variables the same way Pi Coding Agent does, run the Role
// Manager pipeline over the user prompt, and return the completion text.
package run

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/vulnetix/signet/internal/delimiters"
	"github.com/vulnetix/signet/internal/httpclient"
	"github.com/vulnetix/signet/internal/nonce"
	"github.com/vulnetix/signet/internal/prompt"
	"github.com/vulnetix/signet/internal/resilience"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/sanitize"
	"github.com/vulnetix/signet/internal/transcript"
	"github.com/vulnetix/signet/internal/wire"
)

// Chunk is one piece of a streamed response.
type Chunk struct {
	Text      string
	Reasoning string
	ToolCall  *ToolCallDelta // render-only; never carries execution authority
	Err       error
	Done      bool
	Usage     *transcript.Usage // set on the Done chunk when the provider reported it
	Assistant *Assistant        // set on the Done chunk when the turn completed
}

// Stream sends a conversation and returns a channel of text deltas.
// Cancellation runs through ctx. The channel is always closed exactly once.
func Stream(ctx context.Context, cfg Config, turns []Turn, client *http.Client) (<-chan Chunk, error) {
	return StreamWithPool(ctx, cfg, turns, client, nonce.New(), prompt.Options{})
}

// StreamWithPool is Stream with a caller-provided nonce pool.
func StreamWithPool(ctx context.Context, cfg Config, turns []Turn, client *http.Client, pool *nonce.Pool, opts prompt.Options) (<-chan Chunk, error) {
	if client == nil {
		client = httpclient.Default()
	}
	if pool == nil {
		pool = nonce.New()
	}

	verifiedSystem, err := SealSystem(cfg, pool, opts)
	if err != nil {
		return nil, err
	}

	return streamTurns(ctx, cfg, verifiedSystem, turns, client, pool, nil, nil)
}

// StreamTurnsWithTools streams a conversation with tool definitions, taking an
// already-sealed system prompt. Sealing happens once, before the first
// connect; re-sealing each iteration would mint fresh nonces and invalidate
// the sealed system block mid-conversation.
func StreamTurnsWithTools(ctx context.Context, cfg Config, system string, turns []Turn, client *http.Client, pool *nonce.Pool, openAITools []wire.OpenAITool, anthropicTools []wire.AnthropicToolDef) (<-chan Chunk, error) {
	if client == nil {
		client = httpclient.Default()
	}
	if pool == nil {
		pool = nonce.New()
	}
	return streamTurns(ctx, cfg, system, turns, client, pool, openAITools, anthropicTools)
}

// SendTurnsStreamed adapts the blocking sender to the same Chunk channel so a
// caller can consume both transports through one interface.
func SendTurnsStreamed(ctx context.Context, cfg Config, system string, turns []Turn, client *http.Client, pool *nonce.Pool, openAITools []wire.OpenAITool, anthropicTools []wire.AnthropicToolDef) <-chan Chunk {
	ch := make(chan Chunk, 256)
	go func() {
		defer close(ch)
		if pool == nil {
			pool = nonce.New()
		}
		turns = egressTurns(turns, pool)
		a, err := sendTurnsWithTools(ctx, cfg, system, turns, client, openAITools, anthropicTools)
		if err != nil {
			ch <- Chunk{Err: err, Done: true}
			return
		}
		if a.Text != "" {
			ch <- Chunk{Text: a.Text}
		}
		ch <- Chunk{Done: true, Usage: a.Usage, Assistant: &a}
	}()
	return ch
}

// egressTurns sanitises and egress-verifies every turn, then seals any SAFE
// attachments into the same turn after the sanitise pass. Tool-call metadata
// is preserved so the tool round-trip survives. Sanitise/Egress are idempotent
// on already-processed content.
//
// Attachment nonces are reserved per call and deliberately not released: a
// previously sealed block must still verify if Egress runs over the same
// content again, which is what the idempotency guarantee above rests on. The
// cost is that pool.active grows by one per attachment per provider call for
// the life of the session. Attachments are per-prompt and few, so the bound is
// small — but it is a real bound, not zero.
func egressTurns(turns []Turn, pool *nonce.Pool) []Turn {
	out := make([]Turn, len(turns))
	for i, t := range turns {
		content := sanitize.Sanitize(t.Content)
		// The directive is sealed first so it reads as the framing for whatever
		// follows in the same turn.
		if t.Directive != "" {
			if nonceVal, err := pool.Reserve(); err == nil {
				sealed := delimiters.Wrap(delimiters.KindDirective, nonceVal, sanitize.Sanitize(t.Directive))
				if content == "" {
					content = sealed
				} else {
					content = sealed + "\n" + content
				}
			}
			// Fail closed: a directive we cannot seal is dropped rather than
			// sent as bare prose the model could mistake for user instruction.
		}
		for _, att := range t.Attachments {
			nonceVal, err := pool.Reserve()
			if err != nil {
				// Fail closed: drop attachments we cannot seal.
				continue
			}
			body := sanitize.Sanitize(att.Kind + ":" + att.Label + "\n" + att.Body)
			content += "\n" + delimiters.Wrap(delimiters.KindAttachment, nonceVal, body)
		}
		out[i] = Turn{
			Role:       t.Role,
			Content:    delimiters.Egress(content, pool),
			ToolCalls:  t.ToolCalls,
			ToolCallID: t.ToolCallID,
			ToolName:   t.ToolName,
		}
	}
	return out
}

// openStream builds a streaming request and waits for a 2xx response. It is
// the retryable, pre-first-byte half of the streaming path: a failed
// attempt drains and closes the response body before returning so the
// backoff sleep does not hold a live connection.
func openStream(ctx context.Context, cfg Config, system string, turns []Turn, client *http.Client, openAITools []wire.OpenAITool, anthropicTools []wire.AnthropicToolDef) (*http.Response, dialect, error) {
	factory, d, err := newRequestFactory(cfg, system, turns, true, openAITools, anthropicTools)
	if err != nil {
		return nil, d, err
	}
	redact := func(s string) string {
		return strings.ReplaceAll(s, cfg.APIKey, "<redacted>")
	}

	do := func(ctx context.Context) (*http.Response, error) {
		req, err := factory(ctx)
		if err != nil {
			return nil, err
		}
		req.Header.Set("accept", "text/event-stream")
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("request: %w", err)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			return nil, newProviderError("openStream", cfg, resp, body, redact)
		}
		return resp, nil
	}

	resp, err := resilience.Do(ctx, defaultRetryPolicy, resilience.DefaultClassifier{}, do, nil)
	return resp, d, err
}

// drainStream reads an already-open SSE response until completion or error.
// It always closes resp.Body and closes ch exactly once. An idle-gap watchdog
// wraps the body so a provider that stops producing bytes mid-stream is torn
// down after httpclient.StreamIdleTimeout instead of hanging the turn forever.
func drainStream(ctx context.Context, ch chan<- Chunk, resp *http.Response, d dialect) {
	defer close(ch)
	defer resp.Body.Close()

	wd := newIdleWatchdog(resp.Body, httpclient.StreamIdleTimeout)
	wd.start(func() { resp.Body.Close() })
	defer wd.stop()

	scan := bufio.NewScanner(wd)
	scan.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	acc := newToolAccumulator()
	var text strings.Builder
	var reasoning strings.Builder
	var usage *transcript.Usage
	var calls []rolemanager.ToolCall
	var stopReason string

	send := func(c Chunk) bool {
		select {
		case ch <- c:
			return true
		case <-ctx.Done():
			return false
		}
	}

	sendDone := func() {
		if d.kind == kindWorkersAI && len(acc.calls) > 0 {
			if completed, err := acc.completeAll(); err == nil {
				calls = append(calls, completed...)
			}
		}
		send(Chunk{Done: true, Usage: usage, Assistant: &Assistant{Text: text.String(), Reasoning: reasoning.String(), ToolCalls: calls, Usage: usage, StopReason: stopReason}})
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
		delta, err := decodeStreamEvent(d, data, acc)
		if err != nil {
			send(Chunk{Err: err, Done: true})
			return
		}
		if delta.usage != nil {
			if usage == nil {
				usage = &transcript.Usage{}
			}
			usage.PromptTokens += delta.usage.PromptTokens
			usage.CompletionTokens += delta.usage.CompletionTokens
			if delta.usage.TotalTokens != 0 {
				usage.TotalTokens = delta.usage.TotalTokens
			}
		}
		if delta.text != "" {
			text.WriteString(delta.text)
			if !send(Chunk{Text: delta.text}) {
				return
			}
		}
		if delta.reasoning != "" {
			reasoning.WriteString(delta.reasoning)
			if !send(Chunk{Reasoning: delta.reasoning}) {
				return
			}
		}
		if delta.toolDelta != nil {
			if !send(Chunk{ToolCall: delta.toolDelta}) {
				return
			}
		}
		for _, c := range delta.completed {
			calls = append(calls, c)
		}
		if delta.stopReason != "" {
			stopReason = delta.stopReason
		}
		select {
		case <-ctx.Done():
			send(Chunk{Err: ctx.Err(), Done: true})
			return
		default:
		}
	}
	if err := scan.Err(); err != nil {
		if wd.fired() {
			send(Chunk{Err: fmt.Errorf("stream idle timeout after %s", httpclient.StreamIdleTimeout), Done: true})
			return
		}
		if !send(Chunk{Err: fmt.Errorf("stream read: %w", err), Done: true}) {
			return
		}
		return
	}
	sendDone()
}

// idleWatchdog wraps a response body so a stream that stops producing bytes is
// torn down after a gap. Reset on every successful read; on expiry it closes
// the body, which unblocks the pending read with an error.
type idleWatchdog struct {
	r         io.Reader
	d         time.Duration
	t         *time.Timer
	firedFlag atomic.Bool
}

func newIdleWatchdog(r io.Reader, d time.Duration) *idleWatchdog {
	return &idleWatchdog{r: r, d: d}
}

func (w *idleWatchdog) Read(p []byte) (int, error) {
	n, err := w.r.Read(p)
	if n > 0 && w.t != nil {
		w.t.Reset(w.d)
	}
	return n, err
}

func (w *idleWatchdog) start(closeFn func()) {
	w.t = time.AfterFunc(w.d, func() {
		w.firedFlag.Store(true)
		closeFn()
	})
}

func (w *idleWatchdog) stop() {
	if w.t != nil {
		w.t.Stop()
	}
}

func (w *idleWatchdog) fired() bool { return w.firedFlag.Load() }

// streamTurns sends one streaming request with an already-sealed system prompt
// and drains it into Chunks. The retryable openStream call happens in the
// caller's goroutine so an error return means the final attempt failed.
func streamTurns(ctx context.Context, cfg Config, system string, turns []Turn, client *http.Client, pool *nonce.Pool, openAITools []wire.OpenAITool, anthropicTools []wire.AnthropicToolDef) (<-chan Chunk, error) {
	sanitized := egressTurns(turns, pool)
	resp, d, err := openStream(ctx, cfg, system, sanitized, client, openAITools, anthropicTools)
	if err != nil {
		return nil, err
	}
	ch := make(chan Chunk, 256)
	go drainStream(ctx, ch, resp, d)
	return ch, nil
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
