// Package aifirewall implements the gateway-side contract for routing a
// provider through the Vulnetix AI Firewall. It mirrors the shape of the
// ai-firewall provider helper so Belai and the gateway agree on URLs.
package aifirewall

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// DefaultGateway is the production Vulnetix AI Firewall host.
const DefaultGateway = "https://guardrails.vulnetix.com"

// Slug maps a Belai provider name to the gateway's provider path segment.
// Providers with no mapping cannot be routed; the gateway would not know how
// to speak their dialect.
var slugs = map[string]string{
	"anthropic":  "anthropic",
	"openai":     "openai",
	"openrouter": "openrouter",
	"groq":       "groq",
	"mistral":    "mistral",
	"deepseek":   "deepseek",
	"xai":        "xai",
	"together":   "together",
	"fireworks":  "fireworks",
	"alibaba":    "alibaba",
	"moonshot":   "moonshot",
	"minimax":    "minimax",
}

// Slug returns the gateway provider segment for the given Belai provider, and
// whether routing is supported.
func Slug(belaiProvider string) (string, bool) {
	s, ok := slugs[strings.ToLower(belaiProvider)]
	return s, ok
}

// Providers returns the gateway-routable provider names, sorted.
func Providers() []string {
	out := make([]string, 0, len(slugs))
	for name := range slugs {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// BaseURL builds the gateway base URL for a provider slug and org. It appends
// /v1 for every wire except Anthropic messages, where the existing Belai path
// helpers already include it / expect it.
func BaseURL(gateway, slug, orgUUID string) string {
	base := strings.TrimRight(gateway, "/")
	if slug == "anthropic" {
		return fmt.Sprintf("%s/anthropic/%s", base, orgUUID)
	}
	return fmt.Sprintf("%s/%s/%s/v1", base, slug, orgUUID)
}

// IsGatewayURL reports whether baseURL points at the default Vulnetix AI
// Firewall gateway. Self-hosted deployments should set the gateway URL
// explicitly in settings and are detected here by matching the host.
func IsGatewayURL(gatewayURL, baseURL string) bool {
	if gatewayURL != "" {
		return HostOf(baseURL) == HostOf(gatewayURL)
	}
	return HostOf(baseURL) == HostOf(DefaultGateway)
}

// HostOf returns the host component of raw, or "" if raw is not a URL.
func HostOf(raw string) string {
	return hostOf(raw)
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

// URLPathUUID extracts the org UUID from a gateway base URL path of the form
// /{slug}/{org}(/*). It is used by callers that need to recover the org from
// an already-built URL.
func URLPathUUID(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 2 {
		return parts[1]
	}
	return ""
}
