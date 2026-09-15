package run

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/vulnetix/signet/internal/provider"
	"github.com/vulnetix/signet/internal/wire"
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
}

// resolveDialect maps a provider (and model) onto its dialect. The three
// feature bits stay separate from kind: anthropic gets thinking, but the
// gateway relaying a claude model does not, despite both being
// kindAnthropicMessages. Collapsing them would silently change the gateway's
// behaviour.
func resolveDialect(cfg Config) (dialect, error) {
	switch cfg.Provider {
	case "cloudflare-workers-ai":
		return dialect{kind: kindWorkersAI, route: routeWorkersAI}, nil
	case "cloudflare-ai-gateway":
		if isClaudeModel(cfg.Model) {
			return dialect{kind: kindAnthropicMessages, route: routeGateway}, nil
		}
		return dialect{kind: kindOpenAIChat, route: routeGateway}, nil
	case "anthropic":
		return dialect{kind: kindAnthropicMessages, route: routeNative, thinking: true}, nil
	case "openai":
		return dialect{kind: kindOpenAIChat, route: routeNative, effort: true, usage: true}, nil
	default:
		return dialect{}, fmt.Errorf("unknown provider %q", cfg.Provider)
	}
}

// isClaudeModel reports whether a model id routes to a Claude upstream on the
// gateway. The prefix test is case-insensitive.
func isClaudeModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(model), "claude")
}

// streamSurface returns the wire surface whose SSE events this dialect
// decodes.
func (d dialect) streamSurface() wire.Surface {
	if d.kind == kindAnthropicMessages {
		return wire.SurfaceAnthropicMessages
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
