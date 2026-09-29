package run

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"

	"github.com/vulnetix/belai/internal/budget"
	"github.com/vulnetix/belai/internal/transcript"
	"github.com/vulnetix/belai/internal/wire"
)

// UsageEvent reports the tokens one completed model call spent, keyed by the
// provider and model that served it. It is how token budgets see every call —
// the main turn, subagents, and each classifier and role-manager call — from
// one place.
type UsageEvent struct {
	Provider string
	Model    string
	// Tokens is the provider-reported total (prompt + completion, reasoning
	// included), or an estimate when the provider reported none.
	Tokens int
	// Estimated reports that Tokens is the ~4-characters-per-token estimate.
	Estimated bool
	// Role names what made the call: RoleAgent for a main-turn call, or the
	// role-manager use case ("security" for a content classification). It is
	// a harness constant, never model output.
	Role string
	// Prompt, Completion, CacheRead and CacheWrite are the provider-reported
	// components, zero when the provider reported none. CacheRead and
	// CacheWrite are parts of Prompt.
	Prompt     int
	Completion int
	CacheRead  int
	CacheWrite int
	// Request is the estimated composition of what was sent. It holds sizes
	// only — never content — so it may be summarised anywhere usage goes.
	Request RequestShape
	// Limits are the plan limits the call's response headers reported: parsed
	// numbers, identifiers and times, never header text. Empty for a provider
	// that reports none.
	Limits []budget.PlanLimit
}

// RequestShape is the estimated token size of each part of one request, at
// ~4 characters per token.
type RequestShape struct {
	System   int
	ToolDefs int
	// History is every turn except tool results.
	History int
	// ToolResults is the tool-result turns, keyed by tool name (a harness
	// identifier).
	ToolResults map[string]int
}

// Usage roles that are not role-manager use cases.
const (
	// RoleAgent is a main-turn call: the agent, a subagent or an explorer.
	RoleAgent = "agent"
	// RoleSecurity is a content classification (the security payload carries
	// no use case).
	RoleSecurity = "security"
)

// UsageObserver receives one UsageEvent per completed model call. It is called
// on the goroutine that made the call, so it must not block.
type UsageObserver func(UsageEvent)

var (
	usageObserver  atomic.Pointer[UsageObserver]
	telemetryUsage atomic.Pointer[UsageObserver]
	usageMu        sync.Mutex
)

// usageRoleKey keys the usage role on a model call's context.
type usageRoleKey struct{}

// withUsageRole marks every model call made under ctx as made for role.
func withUsageRole(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, usageRoleKey{}, role)
}

// usageRole returns the role ctx was marked with, or RoleAgent.
func usageRole(ctx context.Context) string {
	if ctx != nil {
		if r, ok := ctx.Value(usageRoleKey{}).(string); ok && r != "" {
			return r
		}
	}
	return RoleAgent
}

// SetUsageObserver registers the process-wide usage observer and returns a
// cancel that detaches it. Registering again replaces the previous observer;
// SetUsageObserver(nil) detaches immediately. The cancel is idempotent and
// only detaches the observer it registered.
func SetUsageObserver(fn UsageObserver) (cancel func()) {
	usageMu.Lock()
	defer usageMu.Unlock()
	if fn == nil {
		usageObserver.Store(nil)
		return func() {}
	}
	usageObserver.Store(&fn)
	var once sync.Once
	return func() {
		once.Do(func() {
			usageObserver.CompareAndSwap(&fn, nil)
		})
	}
}

// reportUsage tells the observer what one completed call spent. A call that
// fails or is cancelled before it completes reports nothing: providers send
// usage only with the completed response.
func reportUsage(ctx context.Context, cfg Config, system string, turns []Turn, a Assistant, toolDefs int, limits []budget.PlanLimit) {
	obs, tel := usageObserver.Load(), telemetryUsage.Load()
	if obs == nil && tel == nil {
		return
	}
	tokens, estimated := callTokens(system, turns, a)
	if tokens <= 0 {
		return
	}
	ev := UsageEvent{
		Provider:  cfg.Provider,
		Model:     cfg.Model,
		Tokens:    tokens,
		Estimated: estimated,
		Role:      usageRole(ctx),
		Request:   requestShape(system, turns, toolDefs),
		Limits:    limits,
	}
	if a.Usage != nil {
		ev.Prompt = a.Usage.PromptTokens
		ev.Completion = a.Usage.CompletionTokens
		ev.CacheRead = a.Usage.CacheReadTokens
		ev.CacheWrite = a.Usage.CacheWriteTokens
	}
	if obs != nil {
		(*obs)(ev)
	}
	if tel != nil {
		(*tel)(ev)
	}
}

// requestShape estimates the size of each part of a request.
func requestShape(system string, turns []Turn, toolDefs int) RequestShape {
	s := RequestShape{
		System:   transcript.EstimateTokens(transcript.Message{Role: "system", Content: system}),
		ToolDefs: toolDefs,
	}
	for _, t := range turns {
		n := transcript.EstimateTokens(transcript.Message{Role: t.Role, Content: t.Content})
		for _, c := range t.ToolCalls {
			n += toolCallTokens(c.Name, c.Args)
		}
		if t.Role != "tool" {
			s.History += n
			continue
		}
		if s.ToolResults == nil {
			s.ToolResults = map[string]int{}
		}
		name := t.ToolName
		if name == "" {
			name = "unknown"
		}
		s.ToolResults[name] += n
	}
	return s
}

// toolDefTokens estimates the size of the tool definitions sent with a
// request, whichever surface carries them.
func toolDefTokens(openAITools []wire.OpenAITool, anthropicTools []wire.AnthropicToolDef) int {
	var v any
	switch {
	case len(anthropicTools) > 0:
		v = anthropicTools
	case len(openAITools) > 0:
		v = openAITools
	default:
		return 0
	}
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return (len(b) + 3) / 4
}

// callTokens returns the provider-reported total for a call, or, when the
// provider reported none, an estimate over everything sent and received:
// the system prompt, every turn, and the reply's text, reasoning and tool
// calls.
func callTokens(system string, turns []Turn, a Assistant) (int, bool) {
	if a.Usage != nil {
		if n := a.Usage.Total(); n > 0 {
			return n, false
		}
	}
	n := transcript.EstimateTokens(transcript.Message{Role: "system", Content: system})
	for _, t := range turns {
		n += transcript.EstimateTokens(transcript.Message{Role: t.Role, Content: t.Content})
		for _, c := range t.ToolCalls {
			n += toolCallTokens(c.Name, c.Args)
		}
	}
	n += transcript.EstimateTokens(transcript.Message{Role: "assistant", Content: a.Text + a.Reasoning})
	for _, c := range a.ToolCalls {
		n += toolCallTokens(c.Name, c.Args)
	}
	return n, true
}

func toolCallTokens(name string, args map[string]any) int {
	n := len(name)
	for k, v := range args {
		n += len(k)
		if s, ok := v.(string); ok {
			n += len(s)
		} else {
			n += 8
		}
	}
	return (n + 3) / 4
}

// SetTelemetryUsage registers a second, independent usage observer for
// OpenTelemetry export, so it never displaces the budget ledger's. nil
// detaches it.
func SetTelemetryUsage(fn UsageObserver) {
	if fn == nil {
		telemetryUsage.Store(nil)
		return
	}
	telemetryUsage.Store(&fn)
}
