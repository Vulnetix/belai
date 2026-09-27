package kiroauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	oidcRequestLimit = 15 * time.Second
	maxOIDCBody      = 64 << 10
)

// oidcClient speaks the three SSO-OIDC operations Belai needs.
type oidcClient struct {
	// base overrides the endpoint; empty means OIDCBaseURL(region).
	base   string
	client *http.Client
}

// oidcError is the SSO-OIDC error body.
type oidcError struct {
	Error       string `json:"error"`
	Description string `json:"error_description"`
}

func (o oidcClient) endpoint(region string) (string, error) {
	base := o.base
	if base == "" {
		if !ValidRegion(region) {
			return "", fmt.Errorf("invalid AWS region %q", region)
		}
		base = OIDCBaseURL(region)
	}
	base = strings.TrimRight(base, "/")
	if !AllowedOIDCURL(base) {
		return "", fmt.Errorf("refusing to send a Kiro login to %q", base)
	}
	return base, nil
}

// httpClient returns a client that never follows a redirect: a token request
// answered with a 3xx is an error, not a hop to another host.
func (o oidcClient) httpClient() *http.Client {
	var c http.Client
	if o.client != nil {
		c = *o.client
	} else {
		c.Timeout = oidcRequestLimit
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

// post sends body to path and decodes a 2xx reply into out. A non-2xx reply
// returns its status and the decoded error body.
func (o oidcClient) post(ctx context.Context, region, path string, body, out any) (int, oidcError, error) {
	base, err := o.endpoint(region)
	if err != nil {
		return 0, oidcError{}, err
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return 0, oidcError{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(payload))
	if err != nil {
		return 0, oidcError{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := o.httpClient().Do(req)
	if err != nil {
		return 0, oidcError{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxOIDCBody))
	if err != nil {
		return resp.StatusCode, oidcError{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e oidcError
		_ = json.Unmarshal(data, &e)
		return resp.StatusCode, e, nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return resp.StatusCode, oidcError{}, fmt.Errorf("malformed response from the AWS sign-in service: %w", err)
	}
	return resp.StatusCode, oidcError{}, nil
}

type registration struct {
	ClientID              string `json:"clientId"`
	ClientSecret          string `json:"clientSecret"`
	ClientSecretExpiresAt int64  `json:"clientSecretExpiresAt"`
}

func (o oidcClient) register(ctx context.Context, region, startURL string) (registration, error) {
	var r registration
	status, oe, err := o.post(ctx, region, "/client/register", map[string]any{
		"clientName": "belai",
		"clientType": "public",
		"scopes":     Scopes,
		"grantTypes": []string{"urn:ietf:params:oauth:grant-type:device_code", "refresh_token"},
		"issuerUrl":  startURL,
	}, &r)
	if err != nil {
		return registration{}, fmt.Errorf("could not register with the AWS sign-in service: %w", err)
	}
	if status < 200 || status >= 300 {
		return registration{}, fmt.Errorf("the AWS sign-in service refused the client registration (HTTP %d %s)", status, safeCode(oe.Error))
	}
	if r.ClientID == "" || r.ClientSecret == "" {
		return registration{}, errors.New("incomplete client registration from the AWS sign-in service")
	}
	return r, nil
}

type tokenResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    int64  `json:"expiresIn"`
}

// safeCode keeps an OIDC error code to identifier characters so a hostile
// response cannot put arbitrary text into a message.
func safeCode(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		}
		if b.Len() >= 40 {
			break
		}
	}
	return b.String()
}
