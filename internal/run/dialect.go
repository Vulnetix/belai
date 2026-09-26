package run

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/vulnetix/belai/internal/provider"
	"github.com/vulnetix/belai/internal/wire"
)

// kind is the wire shape a provider speaks for this (provider, model) pair.
type kind uint8

const (
	kindOpenAIChat        kind = iota // OpenAI chat/completions shape
	kindAnthropicMessages             // Anthropic messages shape
	kindWorkersAI                     // Cloudflare Workers AI /ai/run/{model}
)

// route is how a request reaches the wire surface.
type route uint8

const (
	routeNative    route = iota // direct to the provider base URL
	routeGateway                // through the Cloudflare AI Gateway passthrough
	routeWorkersAI              // Workers AI /ai/run/{model}
)

// dialect is the resolved wire behaviour for one (provider, model) pair,
// resolved once in buildRequest and carried to the parse and decode sites so
// the three can never disagree.
type dialect struct {
	kind     kind
	route    route
	thinking bool // emit wire.AnthropicThinking from Effort
	effort   bool // emit reasoning_effort from Effort
	usage    bool // emit stream_options.include_usage when streaming
	method   wire.ToolMethod
	// cache marks Anthropic prompt-cache breakpoints. Native Anthropic only:
	// an Anthropic-shaped custom server or relay may reject the field.
	cache bool
	// maxCompletion sends the completion cap as max_completion_tokens.
	maxCompletion bool
}

// resolveDialect maps a provider (and model) onto its dialect. The three
// feature bits stay separate from kind: anthropic gets thinking, but the
// gateway relaying a claude model does not, despite both being
// kindAnthropicMessages. Collapsing them would silently change the gateway's
// behaviour.
// ResolveDialect returns the wire dialect for a provider/model pair, ignoring
// any error and returning a zero dialect instead. It is exported for callers
// that only need feature predicates such as UsesEffort.
func ResolveDialect(cfg Config) dialect {
	d, _ := resolveDialect(cfg)
	return d
}

func resolveDialect(cfg Config) (dialect, error) {
	if cfg.API != "" {
		return customDialect(cfg.API)
	}
	d, ok := provider.Lookup(cfg.Provider)
	if !ok {
		return dialect{}, fmt.Errorf("unknown provider %q", cfg.Provider)
	}
	out := dialect{
		kind:     surfaceKind(d.Surface),
		route:    routeNative,
		thinking: d.Thinking,
		effort:   d.Effort,
		usage:    d.Usage,
		method:   d.ToolMethod,

		cache:         d.PromptCache,
		maxCompletion: d.MaxCompletionTokens,
	}
	// Workers AI has its own route and requires tool arguments as objects.
	if cfg.Provider == "cloudflare-workers-ai" {
		return dialect{kind: kindWorkersAI, route: routeWorkersAI, method: wire.ToolMethodObject}, nil
	}
	// Cloudflare AI Gateway switches surface based on the upstream model.
	// This split is intentionally retained here rather than encoded in the
	// descriptor so the gateway's Anthropic branch does not pick up the
	// native Anthropic thinking feature.
	if cfg.Provider == "cloudflare-ai-gateway" {
		if isClaudeModel(cfg.Model) {
			return dialect{kind: kindAnthropicMessages, route: routeGateway}, nil
		}
		return dialect{kind: kindOpenAIChat, route: routeGateway, method: out.method}, nil
	}
	return out, nil
}

// surfaceKind maps a descriptor surface to the internal wire kind.
func surfaceKind(s wire.Surface) kind {
	switch s {
	case wire.SurfaceAnthropicMessages:
		return kindAnthropicMessages
	case wire.SurfaceWorkersAI:
		return kindWorkersAI
	}
	return kindOpenAIChat
}

// customDialect maps a custom profile's wire surface onto a dialect. It
// rejects openai-responses rather than silently downgrading: run has no
// parser or stream decoder for that surface, so admitting it would produce
// empty replies.
func customDialect(s wire.Surface) (dialect, error) {
	switch s {
	case wire.SurfaceOpenAIChat:
		return dialect{kind: kindOpenAIChat, route: routeNative, method: wire.ToolMethodString}, nil
	case wire.SurfaceAnthropicMessages:
		return dialect{kind: kindAnthropicMessages, route: routeNative}, nil
	default:
		return dialect{}, fmt.Errorf("custom provider surface %q is not supported", s)
	}
}

// isClaudeModel reports whether a model id routes to a Claude upstream on the
// gateway. The prefix test is case-insensitive.
func isClaudeModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(model), "claude")
}

// UsesEffort reports whether this dialect actually emits the reasoning_effort
// or thinking.budget_tokens fields. When false, the UI should warn that the
// chosen effort level is not sent on the wire for this provider.
func (d dialect) UsesEffort() bool {
	return d.thinking || d.effort
}

// envVarForProvider returns the conventional environment variable holding a
// custom provider's API key: BELAI_<UPPER_SNAKE_NAME>_API_KEY.
func envVarForProvider(providerName string) string {
	return "BELAI_" + upperSnake(providerName) + "_API_KEY"
}

func upperSnake(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
			b.WriteByte(c - 'a' + 'A')
		case c >= '0' && c <= '9':
			b.WriteByte(c)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// streamSurface returns the wire surface whose SSE events this dialect
// decodes.
func (d dialect) streamSurface() wire.Surface {
	switch d.kind {
	case kindAnthropicMessages:
		return wire.SurfaceAnthropicMessages
	case kindWorkersAI:
		return wire.SurfaceWorkersAI
	}
	return wire.SurfaceOpenAIChat
}

// chatRequest builds an OpenAI chat/completions request via the dialect's
// route.
func (d dialect) chatRequest(p *provider.Provider, body wire.OpenAIChatRequest) (*http.Request, error) {
	if d.route == routeGateway {
		return p.NewGatewayChatRequest(body)
	}
	return p.NewChatRequest(body)
}

// messagesRequest builds an Anthropic messages request via the dialect's route.
func (d dialect) messagesRequest(p *provider.Provider, body wire.AnthropicMessagesRequest) (*http.Request, error) {
	if d.route == routeGateway {
		return p.NewGatewayMessagesRequest(body)
	}
	return p.NewMessagesRequest(body)
}
