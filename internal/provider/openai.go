package provider

import (
	"net/http"

	"github.com/vulnetix/signet/internal/wire"
)

// NewChatRequest builds an OpenAI chat/completions request. Streaming is
// selected via body.Stream; the wire shape carries both streaming and
// non-streaming fields.
func (p *Provider) NewChatRequest(body wire.OpenAIChatRequest) (*http.Request, error) {
	return p.newRequest(p.chatURL(), body)
}

// NewResponsesRequest builds an OpenAI responses request.
func (p *Provider) NewResponsesRequest(body wire.OpenAIResponsesRequest) (*http.Request, error) {
	return p.newRequest(p.responsesURL(), body)
}
