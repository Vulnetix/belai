package sessionsync

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// The agent-creator routes live beside the session routes under /v1/belai and
// share the client, so they inherit its origin allowlist, its credential
// handling and its response bounds unchanged. A host calls them only to answer
// an avatar request, and the server accepts each call only for the request that
// names it, while it is delivered to this host.

// MaxAvatar is the largest SVG a host posts back, in bytes.
const MaxAvatar = 64 << 10

// AgentCreator is an avatar request as the website made it: the display name,
// the four colours and an optional personality of the agent being created.
// Every field is untrusted text.
type AgentCreator struct {
	DisplayName string   `json:"displayName"`
	Palette     []string `json:"palette"`
	Personality struct {
		ReportStyle string   `json:"report_style"`
		Focus       []string `json:"focus"`
		Vocabulary  []string `json:"vocabulary"`
	} `json:"personality"`
	// TemplateVersion is the Pix drawing the website expects a variant of.
	TemplateVersion string `json:"templateVersion"`
}

// CreatorFetch reads the avatar request dispatchID names. The server serves it
// only while that request is delivered to this host.
func (c *Client) CreatorFetch(ctx context.Context, hostID, creatorID, dispatchID string) (AgentCreator, error) {
	var out AgentCreator
	path := fmt.Sprintf("/hosts/%s/agent-creators/%s?dispatch=%s", url.PathEscape(hostID), url.PathEscape(creatorID), url.QueryEscape(dispatchID))
	err := c.do(ctx, http.MethodGet, path, nil, &out, requestTimeout)
	return out, err
}

// CreatorAvatar answers an avatar request: the SVG the host drew and nothing
// else, or the reason it did not draw one. svg is empty for a refusal.
func (c *Client) CreatorAvatar(ctx context.Context, hostID, creatorID, dispatchID, svg, refused string) error {
	if len(svg) > MaxAvatar {
		return fmt.Errorf("sessionsync: the avatar is larger than %d bytes", MaxAvatar)
	}
	body := map[string]string{"dispatch": dispatchID}
	if svg != "" {
		body["svg"] = svg
	} else {
		body["refused"] = refused
	}
	return c.do(ctx, http.MethodPost, "/hosts/"+url.PathEscape(hostID)+"/agent-creators/"+url.PathEscape(creatorID)+"/avatar", body, nil, requestTimeout)
}
