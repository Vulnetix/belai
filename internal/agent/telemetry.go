package agent

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/vulnetix/signet/internal/calltrace"
	"github.com/vulnetix/signet/internal/otel"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
)

// run is one turn, wrapped in the signet.turn span and the turn-duration
// histogram. Only the mode, pass count and an outcome word are recorded:
// nothing from the prompt or the reply.
func (s *Session) run(ctx context.Context, history []run.Turn, in TurnInput, streaming bool, emit func(Event)) (run.Result, error) {
	if !otel.Enabled() || s.exploreSubagent {
		return s.runWithClarify(ctx, history, in, streaming, emit)
	}
	start := time.Now()
	span := otel.StartSpan(ctxWithSession(ctx, s.sessionID), "signet.turn")
	mode := "unknown"
	inner := emit
	emit = func(e Event) {
		if e.Kind == EventModeDecidedKind && e.Mode != nil && e.Mode.Mode != "" {
			mode = string(e.Mode.Mode)
		}
		inner(e)
	}
	res, err := s.runWithClarify(ctx, history, in, streaming, emit)
	outcome := turnOutcome(ctx, err)
	span.Set(otel.S(otel.AttrMode, mode), otel.S(otel.AttrOutcome, outcome), otel.I(otel.AttrPasses, int64(res.Passes)))
	if err != nil {
		span.Fail()
	}
	span.End()
	otel.Observe("signet.turn.duration", time.Since(start), otel.S(otel.AttrMode, mode), otel.S(otel.AttrOutcome, outcome))
	return res, err
}

func turnOutcome(ctx context.Context, err error) string {
	var refusal *rolemanager.RefusalError
	var blocked *HookBlockedError
	switch {
	case err == nil:
		return "ok"
	case ctx.Err() != nil:
		return "cancelled"
	case errors.As(err, &refusal):
		return "refused"
	case errors.As(err, &blocked):
		return "hook_blocked"
	}
	return "error"
}

// executeCall is one tool call, wrapped in the signet.tool_call span and the
// tool-call counter. The tool's name and kind and an outcome word are
// recorded; its arguments and result never are.
func (s *Session) executeCall(ctx context.Context, call rolemanager.ToolCall, emit func(Event), eff *callEffect) string {
	if !otel.Enabled() {
		return s.executeCallInner(ctx, call, emit, eff)
	}
	kind := "unknown"
	if t, ok := s.registry.Find(call.Name); ok {
		kind = string(t.Kind())
	}
	span := otel.StartSpan(ctxWithSession(ctx, s.sessionID), "signet.tool_call", otel.S(otel.AttrToolName, call.Name), otel.S(otel.AttrToolKind, kind))
	out := s.executeCallInner(ctx, call, emit, eff)
	outcome := toolOutcome(out)
	span.Set(otel.S(otel.AttrOutcome, outcome))
	if outcome != "ok" {
		span.Fail()
	}
	span.End()
	otel.Add("signet.tool_calls", 1, otel.S(otel.AttrToolKind, kind), otel.S(otel.AttrOutcome, outcome))
	return out
}

func toolOutcome(out string) string {
	switch {
	case strings.HasPrefix(out, "tool result withheld: denied by hook"):
		return "hook_denied"
	case strings.HasPrefix(out, "tool result withheld: permission denied"):
		return "denied"
	case strings.HasPrefix(out, "tool result withheld: classified"):
		return "classified_unsafe"
	case strings.HasPrefix(out, "tool result withheld"):
		return "withheld"
	case strings.HasPrefix(out, "tool call rejected"):
		return "rejected"
	}
	return "ok"
}

// ctxWithSession makes sure the span joins the session's trace.
func ctxWithSession(ctx context.Context, sessionID string) context.Context {
	if sessionID == "" {
		return ctx
	}
	if calltrace.SessionID(ctx) != "" {
		return ctx
	}
	return calltrace.WithSession(ctx, sessionID)
}
