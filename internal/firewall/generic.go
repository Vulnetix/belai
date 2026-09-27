package firewall

import (
	"strings"

	"github.com/vulnetix/belai/internal/config"
)

// requestIDHeaders are the request-id headers gateways commonly set.
var requestIDHeaders = []string{HeaderRequestID, "X-Request-Id", "X-Kong-Request-Id", "Cf-Aig-Log-Id", "Fastly-Request-Id", "X-Amzn-Requestid"}

// inspectGeneric recognises the shapes a firewall of unknown make may speak:
// the Vulnetix headers and bodies, the AI Security Gateway's DLP block, and
// any OpenAI- or Anthropic-shaped refusal whose type, code or message names
// a policy. A plain provider error (a bad key, a rate limit) is not a
// firewall event and yields nil.
func inspectGeneric(in InspectInput) *Verdict {
	if v := inspectVulnetix(in); v != nil {
		return v
	}
	if v := inspectAISG(in); v != nil {
		return v
	}
	if in.Status < 400 {
		return nil
	}
	e, ok := parseError(in.Body)
	if !ok {
		return nil
	}
	code := codeString(e.Code)
	if in.Status != 400 && in.Status != 403 && in.Status != 451 && in.Status != 422 {
		return nil
	}
	if !looksLikePolicy(e.Type, code, e.Message) {
		return nil
	}
	return &Verdict{
		Action:    ActionBlock,
		Code:      code,
		Message:   e.Message,
		Rules:     append(stringOrList(e.BlockedBy), e.Metadata.Reasons...),
		RequestID: firstHeader(in.Header, requestIDHeaders...),
		Hint:      "a firewall policy refused this request",
	}
}

type customAdapter struct{}

func (customAdapter) ID() string           { return "custom" }
func (customAdapter) Label() string        { return "Custom firewall" }
func (customAdapter) Stability() Stability { return Stable }
func (customAdapter) Summary() string {
	return "any proxy: transparent, or BYOK with the key in Authorization or a named header"
}
func (customAdapter) Defaults() config.FirewallInstance {
	return config.FirewallInstance{Adapter: "custom", Mode: config.FirewallModeTransparent}
}
func (customAdapter) Modes() []string                    { return allModes }
func (customAdapter) Supports(t Target) (bool, string)   { return supportsCommon(t) }
func (customAdapter) Route(in RouteInput) (Route, error) { return modeRoute(in) }
func (customAdapter) Inspect(in InspectInput) *Verdict   { return inspectGeneric(in) }
func (customAdapter) Instructions() []string {
	return []string{
		"Transparent: only the base URL changes; Belai sends the provider's own key as it would directly.",
		"Authorization: the firewall key is sent as `Authorization: Bearer`; the provider key is never sent (the firewall holds it).",
		"Header: the firewall key is sent in the header you name; the provider key is never sent.",
		"The URL may carry {provider}, replaced with the provider name. OpenAI-style providers expect the URL to end in /v1; Anthropic's does not.",
	}
}

type fastlyAdapter struct{}

func (fastlyAdapter) ID() string           { return "fastly" }
func (fastlyAdapter) Label() string        { return "Fastly AI Runtime Control" }
func (fastlyAdapter) Stability() Stability { return Beta }
func (fastlyAdapter) Summary() string {
	return "ARC virtual keys, budgets and the ARC AI Firewall (log or block mode)"
}
func (fastlyAdapter) Defaults() config.FirewallInstance {
	return config.FirewallInstance{Adapter: "fastly", Mode: config.FirewallModeAuthorization}
}
func (fastlyAdapter) Modes() []string                    { return allModes }
func (fastlyAdapter) Supports(t Target) (bool, string)   { return supportsCommon(t) }
func (fastlyAdapter) Route(in RouteInput) (Route, error) { return modeRoute(in) }
func (fastlyAdapter) Inspect(in InspectInput) *Verdict   { return inspectGeneric(in) }
func (fastlyAdapter) Instructions() []string {
	return []string{
		"Beta: built from Fastly's public ARC documentation and not yet tested against ARC.",
		"Create a virtual key in the Fastly control panel (ARC) and enable the AI Firewall on it; paste the virtual key here.",
		"Set the URL to the ARC endpoint shown for the virtual key. Provider keys stay in ARC.",
	}
}

type kongAdapter struct{}

func (kongAdapter) ID() string           { return "kong" }
func (kongAdapter) Label() string        { return "Kong AI Gateway" }
func (kongAdapter) Stability() Stability { return Beta }
func (kongAdapter) Summary() string {
	return "your Kong route with ai-proxy and the AI prompt-guard, sanitizer or guardrails plugins"
}
func (kongAdapter) Defaults() config.FirewallInstance {
	return config.FirewallInstance{Adapter: "kong", Mode: config.FirewallModeHeader, Header: "apikey"}
}
func (kongAdapter) Modes() []string                    { return allModes }
func (kongAdapter) Supports(t Target) (bool, string)   { return supportsCommon(t) }
func (kongAdapter) Route(in RouteInput) (Route, error) { return modeRoute(in) }
func (kongAdapter) Instructions() []string {
	return []string{
		"Beta: built from Kong's plugin documentation and not yet tested against Kong.",
		"Point the URL at the Kong route (it may carry {provider}). With key-auth the consumer key goes in the `apikey` header; the provider key lives in ai-proxy.",
		"Choose transparent mode when the route forwards the client's own provider key.",
		"ai-prompt-guard refuses with an opaque 400 \"bad request\"; Belai reports it as a probable prompt-guard block.",
	}
}

// Inspect adds Kong's deliberately opaque prompt-guard refusal to the
// generic shapes.
func (kongAdapter) Inspect(in InspectInput) *Verdict {
	if v := inspectGeneric(in); v != nil {
		return v
	}
	e, ok := parseError(in.Body)
	if !ok {
		return nil
	}
	rid := in.Header.Get("X-Kong-Request-Id")
	switch {
	case in.Status == 400 && strings.EqualFold(strings.TrimSpace(e.Message), "bad request"):
		return &Verdict{Action: ActionBlock, Message: e.Message, RequestID: rid,
			Hint: "probably ai-prompt-guard: Kong refuses a guarded prompt with an opaque 400"}
	case in.Status == 403:
		return &Verdict{Action: ActionBlock, Message: e.Message, RequestID: rid,
			Hint: "refused by a Kong AI plugin (semantic prompt guard, content safety or guardrails)"}
	}
	return nil
}
