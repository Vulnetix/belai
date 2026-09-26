// Package kiroauth signs in to Kiro with an AWS Builder ID or an IAM Identity
// Center start URL, and keeps a short-lived Kiro access token fresh.
//
// It runs the AWS SSO-OIDC device authorization grant itself (register a
// public client, start the device authorization, poll the token endpoint)
// so the TUI and the CLI can show the user code. The result is a Login: the
// refresh token and the client registration that can mint access tokens. The
// Login is stored as one credential field (the kiro provider's "login"), and
// the Refresher trades it for an access token before each model request.
//
// Every token goes only to a pinned host: the SSO-OIDC endpoint
// oidc.<region>.amazonaws.com and the Kiro API q.<region>.amazonaws.com or
// codewhisperer.<region>.amazonaws.com, over https and without redirects. The
// device code, client secret, refresh and access tokens are never rendered.
// Nothing here may make a real network call in the test suite.
package kiroauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Defaults for an AWS Builder ID login.
const (
	BuilderIDStartURL = "https://view.awsapps.com/start"
	DefaultRegion     = "us-east-1"
)

// Scopes are the CodeWhisperer scopes Kiro requests.
var Scopes = []string{
	"codewhisperer:completions",
	"codewhisperer:analysis",
	"codewhisperer:conversations",
}

// ErrReloginRequired means the stored login can no longer mint tokens: the
// refresh token was rejected or the client registration expired.
var ErrReloginRequired = errors.New("kiro login expired; sign in again (belai login kiro)")

// Login is everything needed to mint Kiro access tokens. It is secret as a
// whole: the refresh token and client secret are inside it.
type Login struct {
	RefreshToken          string `json:"refresh_token"`
	ClientID              string `json:"client_id"`
	ClientSecret          string `json:"client_secret"`
	ClientSecretExpiresAt int64  `json:"client_secret_expires_at,omitempty"`
	// Region is the SSO-OIDC region the login was made in.
	Region string `json:"region"`
	// StartURL is the Builder ID or IAM Identity Center start URL.
	StartURL string `json:"start_url,omitempty"`
	// APIRegion is the Kiro API region; empty means us-east-1.
	APIRegion string `json:"api_region,omitempty"`
	// ProfileARN is the CodeWhisperer profile an Identity Center login
	// sends with each request. A Builder ID login has none.
	ProfileARN string `json:"profile_arn,omitempty"`
}

// Encode renders the login as the stored credential value.
func (l Login) Encode() string {
	b, _ := json.Marshal(l)
	return string(b)
}

// String never reveals the secrets.
func (l Login) String() string {
	return fmt.Sprintf("kiroauth.Login{Region:%q, StartURL:%q, APIRegion:%q}", l.Region, l.StartURL, l.APIRegion)
}

// GoString never reveals the secrets.
func (l Login) GoString() string { return l.String() }

// ParseLogin decodes and validates a stored login.
func ParseLogin(s string) (Login, error) {
	var l Login
	if err := json.Unmarshal([]byte(strings.TrimSpace(s)), &l); err != nil {
		return Login{}, errors.New("kiro login is not valid JSON; sign in again (belai login kiro)")
	}
	if l.RefreshToken == "" || l.ClientID == "" || l.ClientSecret == "" {
		return Login{}, errors.New("kiro login is incomplete; sign in again (belai login kiro)")
	}
	if l.Region == "" {
		l.Region = DefaultRegion
	}
	if !ValidRegion(l.Region) {
		return Login{}, fmt.Errorf("kiro login has an invalid region %q", l.Region)
	}
	if l.APIRegion != "" && !ValidRegion(l.APIRegion) {
		return Login{}, fmt.Errorf("kiro login has an invalid api region %q", l.APIRegion)
	}
	return l, nil
}

// APIRegionOrDefault returns the Kiro API region.
func (l Login) APIRegionOrDefault() string {
	if l.APIRegion != "" {
		return l.APIRegion
	}
	return DefaultRegion
}

// Expired reports whether the client registration has expired.
func (l Login) Expired(now time.Time) bool {
	return l.ClientSecretExpiresAt > 0 && now.Unix() >= l.ClientSecretExpiresAt
}

var regionRE = regexp.MustCompile(`^[a-z]{2}(-gov|-iso[a-z]?)?-[a-z]+-[0-9]{1,2}$`)

// ValidRegion reports whether r looks like an AWS region name. It keeps an
// attacker-supplied region from steering the pinned hosts anywhere else.
func ValidRegion(r string) bool { return regionRE.MatchString(r) }

// OIDCBaseURL is the SSO-OIDC endpoint for region.
func OIDCBaseURL(region string) string {
	return "https://oidc." + region + ".amazonaws.com"
}

// APIBaseURL is the Kiro API endpoint for region.
func APIBaseURL(region string) string {
	if !ValidRegion(region) {
		region = DefaultRegion
	}
	return "https://q." + region + ".amazonaws.com"
}

var apiHostRE = regexp.MustCompile(`^(q|codewhisperer)\.[a-z0-9-]+\.amazonaws\.com$`)
var oidcHostRE = regexp.MustCompile(`^oidc\.[a-z0-9-]+\.amazonaws\.com$`)

// AllowedAPIURL reports whether an access token may be sent to base: an https
// Kiro API host, or a loopback origin (tests and local mocks).
func AllowedAPIURL(base string) bool { return allowedURL(base, apiHostRE) }

// AllowedOIDCURL is AllowedAPIURL for the SSO-OIDC endpoint.
func AllowedOIDCURL(base string) bool { return allowedURL(base, oidcHostRE) }

func allowedURL(base string, host *regexp.Regexp) bool {
	u, err := url.Parse(base)
	if err != nil || u.User != nil {
		return false
	}
	h := u.Hostname()
	if isLoopback(h) {
		return u.Scheme == "http" || u.Scheme == "https"
	}
	return u.Scheme == "https" && u.Port() == "" && host.MatchString(h)
}

func isLoopback(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// ValidStartURL reports whether s is an acceptable start URL: https with a
// host and no credentials.
func ValidStartURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}
