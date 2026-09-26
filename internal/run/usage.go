package run

import (
	"sync"
	"sync/atomic"

	"github.com/vulnetix/signet/internal/transcript"
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
}

// UsageObserver receives one UsageEvent per completed model call. It is called
// on the goroutine that made the call, so it must not block.
type UsageObserver func(UsageEvent)

var (
	usageObserver  atomic.Pointer[UsageObserver]
	telemetryUsage atomic.Pointer[UsageObserver]
	usageMu        sync.Mutex
)

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
func reportUsage(cfg Config, system string, turns []Turn, a Assistant) {
	obs, tel := usageObserver.Load(), telemetryUsage.Load()
	if obs == nil && tel == nil {
		return
	}
	tokens, estimated := callTokens(system, turns, a)
	if tokens <= 0 {
		return
	}
	ev := UsageEvent{Provider: cfg.Provider, Model: cfg.Model, Tokens: tokens, Estimated: estimated}
	if obs != nil {
		(*obs)(ev)
	}
	if tel != nil {
		(*tel)(ev)
	}
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
