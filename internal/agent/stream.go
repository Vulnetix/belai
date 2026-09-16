package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/vulnetix/signet/internal/permissions"
	"github.com/vulnetix/signet/internal/resilience"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
)

// EventKind identifies one streaming agent event.
type EventKind int

const (
	EventTextKind EventKind = iota
	EventToolCallDeltaKind
	EventToolStartKind
	EventToolResultKind
	EventPermissionAskKind
	EventDoneKind
	EventErrorKind
	// EventReasoningKind carries a streamed reasoning delta (chain-of-thought
	// or thinking blocks) for rendering in a dim, toggleable panel.
	EventReasoningKind
	// EventRetryKind is emitted before each L2 turn retry. It carries the
	// retry attempt number and delay, but no execution authority.
	EventRetryKind
)

// Event is one streaming agent event. Only the fields for the event's Kind are
// meaningful.
type Event struct {
	Kind EventKind

	// Text carries EventText deltas.
	Text string

	// Reasoning carries EventReasoning deltas.
	Reasoning string

	// ToolDelta carries render-only tool-call fragments (EventToolCallDelta).
	ToolDelta *run.ToolCallDelta

	// Tool carries the tool call for EventToolStart.
	Tool *rolemanager.ToolCall

	// ToolName / ToolResult carry EventToolResult.
	ToolName   string
	ToolResult string

	// AskName / AskSubject carry EventPermissionAsk.
	AskName    string
	AskSubject string

	// RetryAttempt and RetryDelay carry EventRetryKind metadata.
	RetryAttempt int
	RetryDelay   time.Duration
	RetryReason  string

	// Err carries EventError.
	Err error

	// Result carries EventDone.
	Result run.Result
}

// RunStream runs the full pipeline on the supplied input (continuing history)
// and streams events on the returned channel. The channel is closed exactly
// once, always ending in EventDone or EventError.
func (s *Session) RunStream(ctx context.Context, history []run.Turn, in TurnInput) <-chan Event {
	if ctx == nil {
		ctx = context.Background()
	}
	ch := make(chan Event)
	go func() {
		defer close(ch)
		res, err := s.run(ctx, history, in, true, func(e Event) {
			select {
			case ch <- e:
			case <-ctx.Done():
			}
		})
		if err != nil {
			ch <- Event{Kind: EventErrorKind, Err: err}
			return
		}
		ch <- Event{Kind: EventDoneKind, Result: res}
	}()
	return ch
}

// streamTurnRetry attempts a single provider turn up to MaxAttempts times,
// emitting EventRetryKind between attempts. It delegates to streamTurn for
// one attempt.
func (s *Session) streamTurnRetry(ctx context.Context, system string, turns []run.Turn, streaming bool, emit func(Event)) (run.Assistant, error) {
	maxAttempts := s.settings.Resilience.MaxAttemptsOr(3)
	policy := resilience.Policy{MaxAttempts: maxAttempts}
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		assistant, err := s.streamTurn(ctx, system, turns, streaming, emit)
		if err == nil {
			return assistant, nil
		}
		lastErr = err
		if attempt == maxAttempts {
			break
		}
		verdict := resilience.DefaultClassifier{}.Classify(err)
		if verdict.Class != resilience.ClassRetryable {
			return run.Assistant{}, maybeCompact(err)
		}
		retryAfter := time.Duration(0)
		if rerr, ok := err.(*run.ProviderError); ok {
			retryAfter = rerr.RetryAfter()
		}
		delay := policy.Delay(attempt-1, retryAfter, policy.Rand())
		emit(Event{
			Kind:         EventRetryKind,
			RetryAttempt: attempt + 1,
			RetryDelay:   delay,
			RetryReason:  err.Error(),
		})
		if sleepErr := policy.Sleep(ctx, delay); sleepErr != nil {
			return run.Assistant{}, sleepErr
		}
	}
	return run.Assistant{}, maybeCompact(lastErr)
}

// streamTurn drains one transport turn into an Assistant, emitting render
// events as chunks arrive.
func (s *Session) streamTurn(ctx context.Context, system string, turns []run.Turn, streaming bool, emit func(Event)) (run.Assistant, error) {
	// Each provider turn gets its own cancel scope so a retry or a UI abort
	// can tear down the producer goroutine without touching the session ctx.
	if ctx == nil {
		ctx = context.Background()
	}
	turnCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		ch  <-chan run.Chunk
		err error
	)
	if streaming {
		ch, err = run.StreamTurnsWithTools(turnCtx, s.cfg, system, turns, s.client, s.pool, s.openAITools, s.anthropicTools)
	} else {
		ch = run.SendTurnsStreamed(turnCtx, s.cfg, system, turns, s.client, s.pool, s.openAITools, s.anthropicTools)
	}
	if err != nil {
		return run.Assistant{}, err
	}

	var text string
	for c := range ch {
		if c.Err != nil {
			return run.Assistant{}, c.Err
		}
		if c.Text != "" {
			text += c.Text
			emit(Event{Kind: EventTextKind, Text: c.Text})
		}
		if c.Reasoning != "" {
			emit(Event{Kind: EventReasoningKind, Reasoning: c.Reasoning})
		}
		if c.ToolCall != nil {
			emit(Event{Kind: EventToolCallDeltaKind, ToolDelta: c.ToolCall})
		}
		if c.Done {
			if c.Assistant != nil {
				return *c.Assistant, nil
			}
			return run.Assistant{Text: text, Usage: c.Usage}, nil
		}
	}
	return run.Assistant{}, fmt.Errorf("stream closed without a done chunk")
}

// permissionDecision evaluates the permission for a tool call before execution
// so the loop can surface EventPermissionAsk. The permission_no_match posture
// gate applies exactly as in executeCall.
func (s *Session) permissionDecision(call rolemanager.ToolCall) permissions.Decision {
	tool, ok := s.registry.Find(call.Name)
	if !ok {
		return permissions.DecisionBlock
	}
	dec, _ := s.decidePermission(call.Name, tool.Subject(call.Args))
	return dec
}
