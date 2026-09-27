package firewall

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/vulnetix/belai/internal/config"
)

// DefaultGateway is the production Vulnetix AI Firewall host.
const DefaultGateway = config.DefaultVulnetixGateway

// Vulnetix AI Firewall response headers (ai-firewall internal/api).
const (
	HeaderDecision   = "X-Vulnetix-Firewall-Decision"
	HeaderRules      = "X-Vulnetix-Firewall-Rules"
	HeaderRedactions = "X-Vulnetix-Firewall-Redactions"
	HeaderStripped   = "X-Vulnetix-Firewall-Stripped"
	HeaderRequestID  = "X-Vulnetix-Firewall-Request-Id"
	HeaderNonce      = "X-Vulnetix-Firewall-Nonce"
	// HeaderNonceMode is the request header a client sends when every sealed
	// block it carries uses a gateway-issued nonce.
	HeaderNonceMode = "X-Vulnetix-Nonce-Mode"
)

// slugs maps a Belai provider name to the gateway's provider path segment.
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
// /v1 for every wire except Anthropic messages, whose SDKs append it.
func BaseURL(gateway, slug, orgUUID string) string {
	base := strings.TrimRight(gateway, "/")
	if slug == "anthropic" {
		return fmt.Sprintf("%s/anthropic/%s", base, orgUUID)
	}
	return fmt.Sprintf("%s/%s/%s/v1", base, slug, orgUUID)
}

// IsGatewayURL reports whether baseURL points at gatewayURL's host (the
// default Vulnetix gateway when gatewayURL is empty).
func IsGatewayURL(gatewayURL, baseURL string) bool {
	if gatewayURL == "" {
		gatewayURL = DefaultGateway
	}
	return HostOf(baseURL) == HostOf(gatewayURL)
}

// HostOf returns the host component of raw, or "" if raw is not a URL.
func HostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

// URLPathUUID extracts the org UUID from a gateway base URL path of the form
// /{slug}/{org}(/*).
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

type vulnetixAdapter struct{}

func (vulnetixAdapter) ID() string           { return "vulnetix" }
func (vulnetixAdapter) Label() string        { return "Vulnetix AI Firewall" }
func (vulnetixAdapter) Stability() Stability { return Stable }
func (vulnetixAdapter) Summary() string {
	return "guardrails, redaction and BYOK provider keys for your Vulnetix organisation"
}
func (vulnetixAdapter) Defaults() config.FirewallInstance {
	return config.FirewallInstance{Adapter: "vulnetix", URL: DefaultGateway}
}
func (vulnetixAdapter) Modes() []string { return nil }
func (vulnetixAdapter) Capabilities() Capabilities {
	return Capabilities{VulnetixCredential: true, KeySync: true, CuratedModels: true}
}

func (vulnetixAdapter) Supports(t Target) (bool, string) {
	if _, ok := Slug(t.Provider); !ok {
		return false, fmt.Sprintf("provider %q cannot be routed through the Vulnetix AI Firewall — routable providers: %s", t.Provider, strings.Join(Providers(), ", "))
	}
	return true, ""
}

// Route keeps the provider's own auth style carrying the Vulnetix API key: the
// gateway reads Authorization: Bearer first, then x-api-key.
func (vulnetixAdapter) Route(in RouteInput) (Route, error) {
	slug, ok := Slug(in.Target.Provider)
	if !ok {
		return Route{}, fmt.Errorf("provider %q cannot be routed through the Vulnetix AI Firewall", in.Target.Provider)
	}
	if in.Account == "" || in.Secret == "" {
		return Route{}, fmt.Errorf("no Vulnetix API key: run `vulnetix auth login` with an API key")
	}
	gw := in.Instance.URL
	if gw == "" {
		gw = DefaultGateway
	}
	if err := config.ValidFirewallURL(gw); err != nil {
		return Route{}, err
	}
	return Route{Mode: "vulnetix", BaseURL: BaseURL(gw, slug, in.Account), APIKey: in.Secret}, nil
}

func (vulnetixAdapter) Instructions() []string {
	return []string{
		"Sign in with `vulnetix auth login` using an API key; Belai reads the key and organisation from the Vulnetix CLI.",
		"Provider keys you save in /providers are pushed to the firewall (BYOK) with `vulnetix ai-firewall key set`, so they are no longer sent from this machine.",
		"Guardrails are managed with `vulnetix ai-firewall guardrails` or at https://www.vulnetix.com/vdb-ai-firewall.",
	}
}

// vulnetixHints explains the gateway's refusal codes.
var vulnetixHints = map[string]string{
	"provider_key_missing": "the firewall has no key for this provider — save it again in /providers to sync it, or run `vulnetix ai-firewall key set`",
	"provider_denied":      "your organisation's AI Firewall policy denies this provider",
	"provider_disabled":    "this provider is disabled on the AI Firewall",
	"model_denied":         "your organisation's AI Firewall policy denies this model — pick another in /model",
	"model_not_allowed":    "this model is not on your organisation's allow list — pick another in /model",
	"invalid_api_key":      "the gateway refused the Vulnetix key — run `vulnetix auth login` with an API key",
	"unsupported_api":      "the gateway does not relay this wire for this provider",
	"unknown_provider":     "the gateway does not know this provider",
	"request_too_large":    "the gateway accepts requests up to 8 MiB",
	"request_blocked":      "a guardrail in your organisation's AI Firewall policy refused this request",
	"tool_denied":          "a capability rule refused a tool this request offered",
	"mcp_denied":           "a capability rule refused an MCP server this request offered",
	"skill_denied":         "a capability rule refused a skill this request offered",
	"client_denied":        "a capability rule refused this client",
	"delimiter_tampered":   "the gateway refused sealed blocks it could not verify",
}

// blockCodes are guardrail refusals; every other code is a refusal.
var blockCodes = map[string]bool{
	"request_blocked": true, "tool_denied": true, "mcp_denied": true, "skill_denied": true,
	"client_denied": true, "delimiter_tampered": true,
}

func (vulnetixAdapter) Inspect(in InspectInput) *Verdict {
	return inspectVulnetix(in)
}

// inspectVulnetix reads the X-Vulnetix-Firewall-* headers and the gateway's
// error bodies. Custom firewalls share it: a self-hosted gateway speaks it.
func inspectVulnetix(in InspectInput) *Verdict {
	h := in.Header
	v := &Verdict{
		RequestID:  h.Get(HeaderRequestID),
		Rules:      splitRules(h.Get(HeaderRules)),
		Redactions: atoi(h.Get(HeaderRedactions)),
		Stripped:   atoi(h.Get(HeaderStripped)),
		Nonce:      parseNonce(h.Get(HeaderNonce)),
	}
	if in.Status >= 400 {
		e, ok := parseError(in.Body)
		if !ok {
			return nil
		}
		code := codeString(e.Code)
		if _, known := vulnetixHints[code]; !known && e.Type != "policy_violation" {
			return nil
		}
		v.Code, v.Message, v.Hint = code, e.Message, vulnetixHints[code]
		v.Action = ActionRefused
		if blockCodes[code] || (e.Type == "policy_violation" && strings.Contains(e.Message, "blocked")) {
			v.Action = ActionBlock
			if v.Hint == "" {
				v.Hint = vulnetixHints["request_blocked"]
			}
		}
		v.Rules = append(v.Rules, stringOrList(e.BlockedBy)...)
		for _, vi := range e.Violations {
			v.Rules = append(v.Rules, vi.PolicyName)
		}
		return v
	}
	switch strings.ToLower(h.Get(HeaderDecision)) {
	case "redact":
		v.Action = ActionRedact
		v.Hint = "matched text was replaced with [REDACTED] before the model saw it"
	case "flag":
		v.Action = ActionFlag
		v.Hint = "the request was forwarded unchanged and recorded in your organisation's inference log"
	}
	if n := v.Nonce; n != nil && n.Mode == "enforce" && (n.Stripped > 0 || n.Tampered > 0) && v.Action == "" {
		v.Action = ActionStrip
		v.Hint = fmt.Sprintf("the gateway stripped %d sealed block(s) it could not verify", max(n.Stripped, n.Tampered))
	}
	if v.Stripped > 0 && v.Action == "" {
		v.Action = ActionStrip
		v.Hint = "a capability rule removed tools or MCP servers before forwarding"
	}
	if v.Action == "" {
		return nil
	}
	return v
}
