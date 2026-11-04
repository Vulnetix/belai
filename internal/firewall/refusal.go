package firewall

import (
	"encoding/json"
	"slices"
	"strings"
)

// A configuration refusal is a gateway saying no for a reason only a person
// can change: the organisation's AI Firewall policy denies the model or the
// provider, the firewall holds no key for the provider, or a quota an
// administrator sets has run out. Waiting or retrying the same call will not
// clear it, and neither will another card, so a fleet worker that meets one
// stops instead of failing card after card (internal/fleet, docs/fleet.md).

// Refusal is a classified configuration refusal.
type Refusal struct {
	// Code is the gateway's code: one of PolicyRefusalCodes, QuotaRefusalCode
	// or BuiltinQuotaCode.
	Code string
	// Quota is a quota refusal rather than a policy one.
	Quota bool
}

// PolicyRefusalCodes are the AI Firewall's policy_violation codes that name a
// model or provider the organisation's policy will not serve.
var PolicyRefusalCodes = []string{"model_denied", "model_not_allowed", "provider_denied", "provider_disabled", "provider_key_missing"}

// QuotaRefusalCode is the AI Firewall's code for a provider quota the
// organisation set running out.
const QuotaRefusalCode = "provider_quota"

// BuiltinQuotaCode is the Pix Sandbox gateway's refusal of the built-in
// models once the organisation's monthly allowance is spent or switched off.
const BuiltinQuotaCode = "builtin_quota"

// Pix Sandbox gateway refusals (website sandbox-worker, workers-ai.ts). That
// gateway answers in the Cloudflare envelope, whose code is the HTTP status,
// so its refusals are known by the fixed messages it composes itself. The
// prefixes are the gateway's own text, never a model's.
var builtinRefusals = []struct {
	prefix string
	r      Refusal
}{
	{"Pix built-in model quota reached", Refusal{Code: BuiltinQuotaCode, Quota: true}},
	{"Pix built-in models are switched off", Refusal{Code: BuiltinQuotaCode, Quota: true}},
	{"only Clef and the Pix built-in models run in a Pix sandbox", Refusal{Code: "model_not_allowed"}},
}

// cloudflareEnvelope is the Workers AI error body.
type cloudflareEnvelope struct {
	Success *bool `json:"success"`
	Errors  []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// ParseRefusal reads a provider error body and reports whether it is a
// configuration refusal. Only the gateway's code is used (the OpenAI and
// Anthropic shaped bodies the AI Firewall sends), or, for the Pix Sandbox
// gateway's Cloudflare envelope, its fixed message prefixes. Any other body,
// a guardrail block included, is not one: a guardrail judges one request's
// content, not the model.
func ParseRefusal(body []byte) (Refusal, bool) {
	if e, ok := parseError(body); ok {
		code := codeString(e.Code)
		switch {
		case code == QuotaRefusalCode, code == BuiltinQuotaCode:
			return Refusal{Code: code, Quota: true}, true
		case slices.Contains(PolicyRefusalCodes, code):
			return Refusal{Code: code}, true
		}
		return Refusal{}, false
	}
	var cf cloudflareEnvelope
	if len(body) == 0 || json.Unmarshal(body, &cf) != nil || cf.Success == nil || *cf.Success {
		return Refusal{}, false
	}
	for _, e := range cf.Errors {
		msg := strings.TrimSpace(e.Message)
		for _, b := range builtinRefusals {
			if strings.HasPrefix(msg, b.prefix) {
				return b.r, true
			}
		}
	}
	return Refusal{}, false
}
