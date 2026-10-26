package sessionsync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Host requests: a headless `belai rc` host asks a person, through the
// website, for something it cannot do itself, instead of opening a browser on
// a machine nobody sits at (docs/remote-control.md, vdb-site
// docs/belai-host-attention.md). The kinds and reasons are the server's closed
// lists; the host picks from them and never sends free text.
const (
	RequestLogin   = "login"
	RequestApprove = "approve"

	ReasonCredentialExpiring = "credential_expiring"
	ReasonReauthNeeded       = "reauth_needed"
	ReasonApprovalNeeded     = "approval_needed"
	ReasonActionNeeded       = "action_needed"
)

// Request states, as the heartbeat reports them.
const (
	RequestPending   = "pending"
	RequestApproved  = "approved"
	RequestDenied    = "denied"
	RequestExpired   = "expired"
	RequestCancelled = "cancelled"
)

// HostRequest is one request as the server describes it.
type HostRequest struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Reason    string `json:"reason,omitempty"`
	Subject   string `json:"subject,omitempty"`
	State     string `json:"state"`
	ExpiresAt int64  `json:"expiresAt"`
}

// HostRequestGrant is the answer to a new request. A login request carries the
// device code of a grant the server bound to this host's principal; it is
// returned once and redeemed (internal/vulnetixcli DeviceLogin.Redeem) after
// the heartbeat reports the request approved.
type HostRequestGrant struct {
	HostRequest
	DeviceCode string `json:"deviceCode,omitempty"`
	Interval   int    `json:"interval,omitempty"`
}

// RequestAttention files a request with the website. ErrNotFound means the
// server predates host requests, ErrUnauthorized that the credential is no
// longer accepted (the server cannot serve a host it cannot identify).
func (c *Client) RequestAttention(ctx context.Context, hostID, kind, reason, subject string) (HostRequestGrant, error) {
	body := map[string]string{"kind": kind, "reason": reason}
	if subject != "" {
		body["subject"] = subject
	}
	var out HostRequestGrant
	err := c.do(ctx, http.MethodPost, "/hosts/"+url.PathEscape(hostID)+"/attention", body, &out, requestTimeout)
	return out, err
}

// BearerExpiry reads the expiry of a "Bearer <jwt>" Authorization value, for
// scheduling only. The signature is not checked: the server decides whether
// the token is good, this only says when to ask for a fresh login. An ApiKey
// has no expiry, and neither does anything malformed.
func BearerExpiry(header string) (time.Time, bool) {
	tok, ok := strings.CutPrefix(header, "Bearer ")
	if !ok {
		return time.Time{}, false
	}
	parts := strings.Split(strings.TrimSpace(tok), ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp float64 `json:"exp"`
	}
	if json.Unmarshal(raw, &claims) != nil || claims.Exp <= 0 {
		return time.Time{}, false
	}
	return time.Unix(int64(claims.Exp), 0), true
}
