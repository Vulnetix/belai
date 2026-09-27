package firewall

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/vulnetix/belai/internal/config"
)

var errNative = errors.New("provider-native guardrails are configured in the provider's dashboard and never route traffic")

// openRouterNative reads OpenRouter's guardrail and moderation refusals. The
// guardrails themselves are set per workspace or per key in OpenRouter.
type openRouterNative struct{}

func (openRouterNative) ID() string           { return "openrouter" }
func (openRouterNative) Label() string        { return "OpenRouter Guardrails" }
func (openRouterNative) Stability() Stability { return Native }
func (openRouterNative) Summary() string {
	return "workspace guardrails: model and provider allow lists, sensitive-info and prompt-injection filters"
}
func (openRouterNative) Defaults() config.FirewallInstance { return config.FirewallInstance{} }
func (openRouterNative) Modes() []string                   { return nil }
func (openRouterNative) Supports(Target) (bool, string)    { return false, errNative.Error() }
func (openRouterNative) Route(RouteInput) (Route, error)   { return Route{}, errNative }
func (openRouterNative) Instructions() []string {
	return []string{
		"Read-only: OpenRouter guardrails apply whenever the openrouter provider is in use.",
		"Configure them at https://openrouter.ai/settings/guardrails — per workspace, per member or per API key.",
		"Options include budgets, model and provider allow lists, zero-data-retention enforcement, sensitive-info redaction or blocking, and prompt-injection detection.",
		"Belai shows a card when OpenRouter refuses a request (HTTP 403 with guardrail or moderation metadata).",
	}
}

func (openRouterNative) Inspect(in InspectInput) *Verdict {
	if in.Status != 403 {
		return nil
	}
	e, ok := parseError(in.Body)
	if !ok {
		return nil
	}
	v := &Verdict{Action: ActionBlock, Code: codeString(e.Code), Message: e.Message}
	switch {
	case len(e.Metadata.Reasons) > 0:
		// Provider moderation: flagged_input is the user's own text and is
		// deliberately not carried.
		v.Rules = e.Metadata.Reasons
		v.Hint = "the upstream provider's moderation flagged this input"
		if e.Metadata.ProviderName != "" {
			v.Hint = e.Metadata.ProviderName + " moderation flagged this input"
		}
	case strings.HasPrefix(e.Message, "Request blocked"):
		v.Rules = e.Metadata.Patterns
		v.Hint = "an OpenRouter guardrail refused this request — see openrouter.ai/settings/guardrails"
	default:
		return nil
	}
	return v
}

// cloudflareNative reads Cloudflare AI Gateway's guardrail and DLP results.
type cloudflareNative struct{}

func (cloudflareNative) ID() string           { return "cloudflare-ai-gateway" }
func (cloudflareNative) Label() string        { return "Cloudflare AI Gateway" }
func (cloudflareNative) Stability() Stability { return Native }
func (cloudflareNative) Summary() string {
	return "Llama Guard guardrails and DLP profiles on your AI Gateway"
}
func (cloudflareNative) Defaults() config.FirewallInstance { return config.FirewallInstance{} }
func (cloudflareNative) Modes() []string                   { return nil }
func (cloudflareNative) Supports(Target) (bool, string)    { return false, errNative.Error() }
func (cloudflareNative) Route(RouteInput) (Route, error)   { return Route{}, errNative }
func (cloudflareNative) Instructions() []string {
	return []string{
		"Read-only: guardrails and DLP apply whenever the cloudflare-ai-gateway provider is in use.",
		"Configure them in the Cloudflare dashboard: AI > AI Gateway > your gateway > Guardrails (flag or block per category) and DLP (profiles per request/response).",
		"Belai shows a card when the gateway blocks a prompt or response (codes 2016/2017), or when DLP flags or blocks (codes 2029/2030 and the cf-aig-dlp header).",
	}
}

var cloudflareCodes = map[string]string{
	"2016": "the gateway's guardrails blocked the prompt",
	"2017": "the gateway's guardrails blocked the response",
	"2029": "a DLP profile blocked the request",
	"2030": "a DLP profile blocked the response",
}

type cfDLP struct {
	Action   string `json:"action"`
	Findings []struct {
		Check   string `json:"check"`
		Profile struct {
			ProfileID string `json:"profile_id"`
		} `json:"profile"`
	} `json:"findings"`
}

func (cloudflareNative) Inspect(in InspectInput) *Verdict {
	rid := firstHeader(in.Header, "Cf-Aig-Log-Id", "Cf-Aig-Event-Id")
	if code := cloudflareCode(in.Body); code != "" {
		return &Verdict{Action: ActionBlock, Code: code, RequestID: rid, Hint: cloudflareCodes[code]}
	}
	raw := in.Header.Get("Cf-Aig-Dlp")
	if raw == "" {
		return nil
	}
	var d cfDLP
	if json.Unmarshal([]byte(raw), &d) != nil {
		return nil
	}
	v := &Verdict{RequestID: rid}
	for _, f := range d.Findings {
		v.Rules = append(v.Rules, strings.ToLower(f.Check)+" "+f.Profile.ProfileID)
	}
	switch strings.ToUpper(d.Action) {
	case "BLOCK":
		v.Action, v.Hint = ActionBlock, "a DLP profile blocked this call"
	case "FLAG":
		v.Action, v.Hint = ActionFlag, "a DLP profile flagged this call; it was forwarded and logged"
	default:
		return nil
	}
	return v
}

// cloudflareCode finds a guardrail or DLP error code anywhere in an error
// body: its envelope is not documented, so any "code" field is checked.
func cloudflareCode(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var root any
	if json.Unmarshal(body, &root) != nil {
		return ""
	}
	var walk func(any, int) string
	walk = func(n any, depth int) string {
		if depth > 6 {
			return ""
		}
		switch t := n.(type) {
		case map[string]any:
			if c, ok := t["code"]; ok {
				var s string
				switch cv := c.(type) {
				case float64:
					s = strconv.FormatFloat(cv, 'f', -1, 64)
				case string:
					s = cv
				}
				if _, ok := cloudflareCodes[s]; ok {
					return s
				}
			}
			for _, v := range t {
				if s := walk(v, depth+1); s != "" {
					return s
				}
			}
		case []any:
			for _, v := range t {
				if s := walk(v, depth+1); s != "" {
					return s
				}
			}
		}
		return ""
	}
	return walk(root, 0)
}
