package provider

import (
	"net/http"

	"github.com/vulnetix/belai/internal/wire"
)

// NewMessagesRequest builds an Anthropic messages request against the
// no-/v1 base URL (the surface path supplies /v1/messages).
func (p *Provider) NewMessagesRequest(body wire.AnthropicMessagesRequest) (*http.Request, error) {
	return p.newRequest(p.messagesURL(), body)
}
