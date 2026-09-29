package firewall

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/vulnetix/belai/internal/sanitize"
)

// Action is what a firewall did to a request.
type Action string

const (
	// ActionBlock: a guardrail refused the request.
	ActionBlock Action = "blocked"
	// ActionRefused: the firewall refused for a non-guardrail reason (a
	// missing provider key, a denied model, a bad credential).
	ActionRefused Action = "refused"
	// ActionRedact: matched text was replaced before the model saw it.
	ActionRedact Action = "redacted"
	// ActionFlag: the request was forwarded unchanged and recorded.
	ActionFlag Action = "flagged"
	// ActionStrip: tools, MCP servers or unverifiable sealed blocks were
	// removed before forwarding.
	ActionStrip Action = "stripped"
)

// Severe reports whether the request did not reach the model.
func (a Action) Severe() bool { return a == ActionBlock || a == ActionRefused }

// NonceReport is the gateway's account of the sealed delimiter blocks it
// checked (X-Vulnetix-Firewall-Nonce).
type NonceReport struct {
	Mode     string // enforce or observe
	Verified int
	Unknown  int
	Tampered int
	Stripped int
}

// Verdict is one firewall event, reduced to harness-parsed facts. Every
// string is cleaned and capped: it is third-party text rendered to the user,
// never sent to a model.
type Verdict struct {
	Firewall   string // adapter label
	Instance   string
	Action     Action
	Code       string
	Message    string
	Rules      []string
	Redactions int
	Stripped   int
	Nonce      *NonceReport
	RequestID  string
	Status     int
	Provider   string
	Model      string
	Hint       string
}

// InspectInput is one response.
type InspectInput struct {
	Provider string
	Model    string
	Status   int
	Header   http.Header
	// Body is the response body for a non-2xx status, or a blocking 2xx
	// response; nil for a stream.
	Body []byte
}

// Inspect runs the route's adapter and the provider's native adapter over one
// response and returns the cleaned verdicts. route may be nil.
func Inspect(route *Route, in InspectInput) []Verdict {
	var out []Verdict
	if route != nil {
		if a, ok := Lookup(route.AdapterID); ok {
			if v := a.Inspect(in); v != nil {
				v.Instance = route.Instance
				out = append(out, v.finish(a, in))
			}
		}
	}
	if a, ok := NativeFor(in.Provider); ok {
		if v := a.Inspect(in); v != nil {
			out = append(out, v.finish(a, in))
		}
	}
	return out
}

func (v *Verdict) finish(a Adapter, in InspectInput) Verdict {
	v.Firewall = a.Label()
	v.Provider, v.Model = cleanIdent(in.Provider, 64), cleanIdent(in.Model, 96)
	if v.Status == 0 {
		v.Status = in.Status
	}
	return v.Clean()
}

// Limits on verdict text.
const (
	maxRules      = 8
	maxRuleRunes  = 60
	maxMessage    = 200
	maxHint       = 200
	maxIdentBytes = 96
)

// Clean returns a copy with every string reduced to safe, capped text.
func (v Verdict) Clean() Verdict {
	v.Firewall = cleanText(v.Firewall, 60)
	v.Instance = cleanIdent(v.Instance, 40)
	v.Code = cleanIdent(v.Code, 48)
	v.Message = cleanText(v.Message, maxMessage)
	v.Hint = cleanText(v.Hint, maxHint)
	v.RequestID = cleanIdent(v.RequestID, maxIdentBytes)
	v.Provider = cleanIdent(v.Provider, 64)
	v.Model = cleanIdent(v.Model, 96)
	var rules []string
	seen := map[string]bool{}
	for _, r := range v.Rules {
		r = cleanText(r, maxRuleRunes)
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		rules = append(rules, r)
		if len(rules) == maxRules {
			break
		}
	}
	v.Rules = rules
	if v.Nonce != nil {
		n := *v.Nonce
		n.Mode = cleanIdent(n.Mode, 16)
		v.Nonce = &n
	}
	return v
}

// cleanText removes delimiter markup, ANSI, control and bidi runes, folds
// whitespace to single spaces and caps the rune count.
func cleanText(s string, max int) string { return sanitize.Clip(s, max) }

// cleanIdent keeps identifier characters only.
func cleanIdent(s string, max int) string {
	var b strings.Builder
	for _, r := range s {
		if r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._:-/@+", r)) {
			b.WriteRune(r)
			if b.Len() >= max {
				break
			}
		}
	}
	return b.String()
}

// atoi parses a non-negative count, capped.
func atoi(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 {
		return 0
	}
	return min(n, 1<<20)
}

// firstHeader returns the first non-empty header among names.
func firstHeader(h http.Header, names ...string) string {
	for _, n := range names {
		if v := h.Get(n); v != "" {
			return v
		}
	}
	return ""
}

// errorEnvelope is the union of the OpenAI and Anthropic error bodies and
// the Vulnetix extensions to them.
type errorEnvelope struct {
	Type  string          `json:"type"`
	Error json.RawMessage `json:"error"`
}

type errorBody struct {
	Message    string          `json:"message"`
	Type       string          `json:"type"`
	Code       json.RawMessage `json:"code"`
	BlockedBy  json.RawMessage `json:"blocked_by"`
	Violations []struct {
		PolicyName string `json:"policy_name"`
		RuleType   string `json:"rule_type"`
		Action     string `json:"action"`
	} `json:"violations"`
	Metadata struct {
		Reasons      []string `json:"reasons"`
		Patterns     []string `json:"patterns"`
		ProviderName string   `json:"provider_name"`
	} `json:"metadata"`
}

// parseError decodes an OpenAI- or Anthropic-shaped error body. ok is false
// when the body is not one.
func parseError(body []byte) (errorBody, bool) {
	var env errorEnvelope
	if len(body) == 0 || json.Unmarshal(body, &env) != nil || len(env.Error) == 0 {
		return errorBody{}, false
	}
	var e errorBody
	if json.Unmarshal(env.Error, &e) != nil {
		// {"error":"pii_policy_violation"} style: a bare string.
		var s string
		if json.Unmarshal(env.Error, &s) != nil {
			return errorBody{}, false
		}
		e.Code = json.RawMessage(strconv.Quote(s))
	}
	return e, true
}

// codeString returns a JSON code that may be a string or a number.
func codeString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}

// stringOrList decodes a value that may be one string or a list of them.
func stringOrList(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "" {
			return nil
		}
		return []string{s}
	}
	var l []string
	_ = json.Unmarshal(raw, &l)
	return l
}

// splitRules decodes a comma-joined list of percent-encoded names.
func splitRules(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if dec, err := url.PathUnescape(part); err == nil {
			part = dec
		}
		out = append(out, part)
	}
	return out
}

// parseNonce decodes "mode=enforce;verified=1;unknown=0;tampered=0;stripped=0".
func parseNonce(v string) *NonceReport {
	if v == "" {
		return nil
	}
	n := &NonceReport{}
	for _, kv := range strings.Split(v, ";") {
		k, val, _ := strings.Cut(strings.TrimSpace(kv), "=")
		switch strings.ToLower(k) {
		case "mode":
			n.Mode = strings.ToLower(val)
		case "verified":
			n.Verified = atoi(val)
		case "unknown":
			n.Unknown = atoi(val)
		case "tampered":
			n.Tampered = atoi(val)
		case "stripped":
			n.Stripped = atoi(val)
		}
	}
	return n
}

// policyWords mark an error body as a guardrail refusal in the generic
// inspector.
var policyWords = []string{"policy", "guardrail", "blocked", "content_filter", "content filter", "prompt guard", "firewall"}

func looksLikePolicy(parts ...string) bool {
	for _, p := range parts {
		l := strings.ToLower(p)
		for _, w := range policyWords {
			if strings.Contains(l, w) {
				return true
			}
		}
	}
	return false
}
