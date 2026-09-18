package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/vulnetix/signet/internal/clarify"
	"github.com/vulnetix/signet/internal/filediff"
	"github.com/vulnetix/signet/internal/goals"
	"github.com/vulnetix/signet/internal/permissions"
	"github.com/vulnetix/signet/internal/resilience"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/todos"
)

// EventKind identifies one streaming agent event.
type EventKind int

const (
	EventTextKind EventKind = iota
	EventToolCallDeltaKind
	EventToolStartKind
	EventToolResultKind
	EventPermissionAskKind
	EventClarifyAskKind
	EventDoneKind
	EventErrorKind
	// EventReasoningKind carries a streamed reasoning delta (chain-of-thought
	// or thinking blocks) for rendering in a dim, toggleable panel.
	EventReasoningKind
	// EventRetryKind is emitted before each L2 turn retry. It carries the
	// retry attempt number and delay, but no execution authority.
	EventRetryKind
	// EventRoleManagerKind signals that the Role Manager is actively
	// classifying content. It carries no execution authority; it exists so a
	// UI can show a dedicated "Role Manager" indicator instead of the generic
	// working label.
	EventRoleManagerKind
	// EventWarningKind carries a non-fatal problem (e.g. a classifier error
	// that caused a tool result to be withheld). It renders as a system line
	// but does not abort the turn, so it is safe to emit mid-pass.
	EventWarningKind
	// EventGoalEvalKind reports a goal-evaluator verdict at a pass boundary.
	// It carries the sentinel and the pass number, no execution authority.
	EventGoalEvalKind
	// EventPassKind reports that a new goal-mode pass started. It carries the
	// pass number and whether a forced explore ran for it.
	EventPassKind
	// EventContinuationKind reports that a bounded agent/plan pass exhausted
	// its tool budget and a continuation pass is about to run. It carries the
	// continuation number and the cap; the TUI renders it as a normal system
	// line, never an error.
	EventContinuationKind
	// EventTodosKind reports a change to the shared todo list (created or
	// advanced). It carries the list; the TUI renders and persists it, the
	// agent never touches the session store.
	EventTodosKind
	// EventGoalStateKind reports an update to the run-time goal state; the TUI
	// persists it as a goal_state session entry.
	EventGoalStateKind
	// EventToolDiffKind carries what a mutating tool changed on disk, keyed by
	// ToolCallID. Render-only, like EventToolProgressKind: it is observed
	// around the tool rather than returned by it, never enters the
	// conversation and never reaches a model.
	EventToolDiffKind
	// EventToolProgressKind carries partial output from a tool that is still
	// running, keyed by ToolCallID. It is render-only and carries no execution
	// authority: it never enters the conversation, never reaches a model, and
	// is superseded by the EventToolResult that follows. A tool that produces
	// no progress emits none, and a consumer may ignore the kind entirely.
	EventToolProgressKind
	// EventCwdKind reports that the session's working directory moved. It is
	// emitted after the tool call that moved it and carries the new location
	// both root-relative and absolute. Informational: it changes nothing the
	// model can see, it tells the UI where later relative paths now point.
	EventCwdKind
	// EventToolMetaKind carries metadata from a tool result, keyed by
	// ToolCallID. Render-only: it never enters the conversation and never
	// reaches a model. Currently used by Read to communicate start_line so
	// partial reads can be numbered correctly.
	EventToolMetaKind
)

// Role Manager sub-phases carried by EventRoleManagerKind.
const (
	RoleManagerPhasePrePrompt  = "pre-prompt"  // admission plus mode selection before the first model turn
	RoleManagerPhaseToolResult = "tool-result" // classification of a tool result before promotion
	RoleManagerPhaseSteer      = "steer"       // admission of a mid-loop steering message
	RoleManagerPhaseClarify    = "clarify"     // interactive questionnaire between explore and planning
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
	// ToolCallID keys a tool result back to the assistant call that requested
	// it. Tool results may now arrive out of order (concurrent read-only
	// tools), so the TUI must match on this rather than the last tool row.
	ToolCallID string

	// ToolProgress carries whole lines of output from a still-running tool on
	// EventToolProgressKind, keyed by ToolCallID. Render-only.
	ToolProgress string

	// Diff carries what a mutating tool changed, on EventToolDiffKind.
	// Render-only.
	Diff *filediff.Change

	// Meta carries tool metadata on EventToolMetaKind. Render-only.
	Meta map[string]any

	// Cwd carries the new working directory on EventCwdKind, relative to the
	// session root and slash-separated; "" means the root itself. CwdDir is
	// the same location as an absolute path, for the footer.
	Cwd    string
	CwdDir string

	// AskName / AskSubject carry EventPermissionAsk.
	AskName    string
	AskSubject string
	// Ask carries the full tool-permission request on EventPermissionAskKind;
	// AskReply is the channel the UI must answer on.
	Ask      *AskRequest
	AskReply chan PermissionAskReply

	// Clarify carries the questionnaire on EventClarifyAskKind; Reply is the
	// channel the UI must send the user's Answers on.
	Clarify *clarify.Questionnaire
	Reply   chan clarify.Answers

	// RetryAttempt and RetryDelay carry EventRetryKind metadata.
	RetryAttempt int
	RetryDelay   time.Duration
	RetryReason  string

	// Phase carries the Role Manager sub-phase for EventRoleManagerKind.
	Phase string

	// GoalSentinel carries EventGoalEvalKind verdicts.
	GoalSentinel rolemanager.GoalSentinel

	// Pass carries the pass number for EventPassKind / EventGoalEvalKind.
	Pass int

	// MaxPasses carries the continuation cap for EventContinuationKind.
	MaxPasses int

	// Explored reports whether a forced explore ran for EventPassKind.
	Explored bool

	// Todos carries the goal-mode todo list when it changes, so the TUI can
	// render and persist it without the agent touching the session store.
	Todos *todos.List

	// GoalState carries an updated run-time goal state on EventGoalStateKind.
	GoalState *goals.GoalState

	// Err carries EventError.
	Err error

	// Warning carries EventWarning.
	Warning string

	// Result carries EventDone.
	Result run.Result
}

// PermissionAskReply is the UI's answer to a tool-permission ask.
type PermissionAskReply struct {
	// Allow is the decision: true executes the call once, false withholds it.
	Allow bool
}

// AskRequest is an interactive tool-permission request emitted on
// EventPermissionAskKind. The UI renders it and answers on AskReply.
type AskRequest struct {
	Name    string
	Subject string
	Args    map[string]any
	// Preview is the diff the call would make, for render only. It is nil
	// when no preview could be produced.
	Preview *filediff.Change
}

// RunStream runs the full pipeline on the supplied input (continuing history)
// and streams events on the returned channel. The channel is closed exactly
// once, always ending in EventDone or EventError.
func (s *Session) RunStream(ctx context.Context, history []run.Turn, in TurnInput) <-chan Event {
	if ctx == nil {
		ctx = context.Background()
	}
	ch := make(chan Event, 256)
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
	// A bare Policy leaves Rand and Sleep nil; this loop reads both as plain
	// fields, so calling them unnormalised would panic on the first retry.
	// Jitter has no non-zero default, so L2 opts into the same 0.25 as the L1
	// transport policy — without it every session retrying one provider wakes
	// together.
	policy := resilience.Policy{MaxAttempts: maxAttempts, Jitter: 0.25}.WithDefaults()
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
	onRetry := func(a resilience.Attempt) {
		emit(Event{
			Kind:         EventRetryKind,
			RetryAttempt: a.Attempt,
			RetryDelay:   a.Delay,
			RetryReason:  a.Reason,
		})
	}
	// The advertised surface follows the mode this turn is running in, so a
	// plan-mode turn never offers a tool executeCall would refuse.
	_, openAITools, anthropicTools := s.toolSurface()
	if streaming {
		ch, err = run.StreamTurnsWithTools(turnCtx, s.cfg, system, turns, s.client, s.pool, openAITools, anthropicTools, onRetry)
	} else {
		ch = run.SendTurnsStreamed(turnCtx, s.cfg, system, turns, s.client, s.pool, openAITools, anthropicTools, onRetry)
	}
	if err != nil {
		return run.Assistant{}, err
	}

	var text strings.Builder
	for c := range ch {
		if c.Err != nil {
			return run.Assistant{}, c.Err
		}
		if c.Text != "" {
			text.WriteString(c.Text)
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
			return run.Assistant{Text: text.String(), Usage: c.Usage}, nil
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
	dec, _, _ := s.decidePermission(call.Name, tool.Subject(call.Args))
	return dec
}
