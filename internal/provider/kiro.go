package provider

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/vulnetix/belai/internal/wire"
)

// NewKiroRequest builds a Kiro generateAssistantResponse request. The caller
// has already checked the base URL against the pinned Kiro hosts.
func (p *Provider) NewKiroRequest(body wire.KiroRequest) (*http.Request, error) {
	if p.auth != AuthKiro {
		return nil, fmt.Errorf("provider %q does not speak the kiro surface", p.name)
	}
	return p.newRequest(strings.TrimRight(p.baseURL, "/")+wire.KiroPath, body)
}
