package firewall

import (
	"encoding/json"
	"fmt"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/wire"
)

// aisgAdapter is the self-hosted AI Security Gateway
// (github.com/aisecuritygateway/aisecuritygateway): an OpenAI chat
// completions proxy with Presidio DLP and prompt-injection detection. The
// provider is chosen with the x-provider request header.
type aisgAdapter struct{}

func (aisgAdapter) ID() string           { return "aisg" }
func (aisgAdapter) Label() string        { return "AI Security Gateway" }
func (aisgAdapter) Stability() Stability { return Beta }
func (aisgAdapter) Summary() string {
	return "self-hosted DLP and prompt-injection gateway (OpenAI chat completions only)"
}
func (aisgAdapter) Defaults() config.FirewallInstance {
	return config.FirewallInstance{Adapter: "aisg", URL: "http://localhost:8000/v1", Mode: config.FirewallModeAuthorization}
}
func (aisgAdapter) Modes() []string { return allModes }

func (aisgAdapter) Supports(t Target) (bool, string) {
	if ok, why := supportsCommon(t); !ok {
		return false, why
	}
	if t.Surface != wire.SurfaceOpenAIChat {
		return false, fmt.Sprintf("the AI Security Gateway serves only OpenAI chat completions; %s speaks %s", t.Provider, t.Surface)
	}
	return true, ""
}

func (aisgAdapter) Route(in RouteInput) (Route, error) {
	r, err := modeRoute(in)
	if err != nil {
		return Route{}, err
	}
	r.Headers = map[string]string{"x-provider": in.Target.Provider}
	return r, nil
}

func (aisgAdapter) Inspect(in InspectInput) *Verdict { return inspectAISG(in) }

func (aisgAdapter) Instructions() []string {
	return []string{
		"Beta: built from the gateway's README and OpenAPI spec and not yet tested against it.",
		"Run the gateway (docker compose up) and set provider keys in its config/gateway.yaml; paste the gateway API key here.",
		"Belai names the provider in the x-provider header. Anthropic's Messages API is not served, so Anthropic goes direct.",
	}
}

type aisgBlock struct {
	Error      string `json:"error"`
	Message    string `json:"message"`
	RequestID  string `json:"request_id"`
	Violations []struct {
		EntityType string `json:"entity_type"`
	} `json:"violations"`
	Detail   json.RawMessage `json:"detail"`
	Metadata *struct {
		PIIDetected     bool     `json:"pii_detected"`
		DLPAction       string   `json:"dlp_action"`
		ViolationsCount int      `json:"violations_count"`
		EntityTypes     []string `json:"entity_types_detected"`
	} `json:"aisg_metadata"`
}

// inspectAISG reads the gateway's DLP block, its credential errors, and the
// aisg_metadata of a blocking success body.
func inspectAISG(in InspectInput) *Verdict {
	var b aisgBlock
	if len(in.Body) == 0 || json.Unmarshal(in.Body, &b) != nil {
		return nil
	}
	rid := firstHeader(in.Header, "X-Request-Id")
	if rid == "" {
		rid = b.RequestID
	}
	switch {
	case in.Status == 400 && b.Error == "pii_policy_violation":
		v := &Verdict{Action: ActionBlock, Code: b.Error, Message: b.Message, RequestID: rid,
			Hint: "the gateway's DLP refused this prompt"}
		for _, vi := range b.Violations {
			v.Rules = append(v.Rules, vi.EntityType)
		}
		return v
	case in.Status == 402:
		return &Verdict{Action: ActionRefused, Code: "no_credentials", RequestID: rid,
			Hint: "the gateway has no provider key for this provider — set it in its config/gateway.yaml"}
	case in.Status == 500 && b.Error == "security_processing_error":
		return &Verdict{Action: ActionRefused, Code: b.Error, Message: b.Message, RequestID: rid,
			Hint: "the gateway's DLP engine is down and it failed closed"}
	case in.Status == 403 && len(b.Detail) > 0:
		var d string
		_ = json.Unmarshal(b.Detail, &d)
		return &Verdict{Action: ActionRefused, Code: "invalid_api_key", Message: d, RequestID: rid,
			Hint: "the gateway refused the key — check it in /firewall"}
	case in.Status < 300 && b.Metadata != nil && b.Metadata.PIIDetected:
		v := &Verdict{RequestID: rid, Rules: b.Metadata.EntityTypes, Redactions: b.Metadata.ViolationsCount}
		switch b.Metadata.DLPAction {
		case "redact":
			v.Action, v.Hint = ActionRedact, "detected PII was replaced with [REDACTED] before the model saw it"
		default:
			v.Action, v.Hint = ActionFlag, "PII was detected and the request was forwarded unchanged"
		}
		return v
	}
	return nil
}
