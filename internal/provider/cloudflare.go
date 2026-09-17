package provider

import (
	"net/http"
	"strings"

	"github.com/vulnetix/signet/internal/wire"
)

// NewWorkersAIRequest builds a Cloudflare Workers AI request:
// POST {base}/ai/run/{model}. The model id (which may contain "/" and "@") is
// appended to the path verbatim, as the Workers AI API expects.
func (p *Provider) NewWorkersAIRequest(model string, body wire.WorkersAIRequest) (*http.Request, error) {
	endpoint := strings.TrimRight(p.baseURL, "/") + "/ai/run/" + model
	return p.newRequest(endpoint, body)
}

// NewGatewayChatRequest builds an OpenAI chat/completions request routed
// through the Cloudflare AI Gateway compatibility endpoint. The base URL ends
// in /compat, so the OpenAI SDK-style /v1/chat/completions path is appended;
// the /openai/chat/completions form is not a supported compatibility endpoint.
func (p *Provider) NewGatewayChatRequest(body wire.OpenAIChatRequest) (*http.Request, error) {
	return p.newRequest(p.gatewayURL("/v1/chat/completions"), body)
}

// NewGatewayResponsesRequest builds an OpenAI responses request routed through
// the Cloudflare AI Gateway /openai passthrough.
func (p *Provider) NewGatewayResponsesRequest(body wire.OpenAIResponsesRequest) (*http.Request, error) {
	return p.newRequest(p.gatewayURL("/openai/responses"), body)
}

// NewGatewayMessagesRequest builds an Anthropic messages request routed through
// the Cloudflare AI Gateway /anthropic passthrough.
func (p *Provider) NewGatewayMessagesRequest(body wire.AnthropicMessagesRequest) (*http.Request, error) {
	return p.newRequest(p.gatewayURL("/anthropic/v1/messages"), body)
}

func (p *Provider) gatewayURL(path string) string {
	return strings.TrimRight(p.baseURL, "/") + path
}
