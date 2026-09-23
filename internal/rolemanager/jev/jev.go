// Package jev implements the Role Manager Jev tool-call gate and the Jev
// security classifier. Both ask the TypeSafe/Jev decision model on
// OpenRouter's Decisions API a set of boolean ("noul") questions and reduce
// the returned probabilities to strict single-token verdicts. Neither turn
// ever carries tools, skills, or an agent block.
package jev

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	openrouter "github.com/OpenRouterTeam/go-sdk"
	"github.com/OpenRouterTeam/go-sdk/models/components"
	"github.com/OpenRouterTeam/go-sdk/models/operations"

	"github.com/vulnetix/signet/internal/rolemanager"
)

// Sentinel is the strict single-token verdict of the Jev tool-call gate.
type Sentinel string

const (
	// Allow means the tool call may execute.
	Allow Sentinel = "ALLOW"
	// Deny means the tool call must not execute.
	Deny Sentinel = "DENY"
	// Inconclusive means the gate could not decide (the probability fell in
	// the review band, or the reply was malformed). The caller fails closed.
	Inconclusive Sentinel = "INCONCLUSIVE"
)

const (
	// DefaultModel is the Jev decision model on OpenRouter's Decisions API.
	// It is a Decisions/System One model, not a chat model, so it never appears
	// in the chat model list.
	DefaultModel = "typesafe/jev-1.13"

	// decisionsServerURL is the OpenRouter Decisions API server root. The SDK
	// appends /api/alpha/decisions to it.
	decisionsServerURL = "https://openrouter.ai"

	// unsafeQuestion keys the single noul question the tool-call gate asks.
	// Its probability is the probability that the proposition is true.
	unsafeQuestion = "unsafe"

	// unsafeInstruction is the proposition Jev evaluates. A high probability
	// means the proposition is true (the call is unsafe); a low probability
	// means it is false (the call is safe).
	unsafeInstruction = "The tool call is unsafe and must be blocked."

	// allowAt is the probability at or below which the proposition is false:
	// the call is safe and may proceed. denyAt is the probability at or above
	// which the proposition is true: the call must be blocked. The band
	// between the two is inconclusive — a human must decide. The thresholds
	// match the OpenRouter "gate tool calls with Jev" cookbook.
	allowAt = 0.1
	denyAt  = 0.9
)

// DefaultServerURL is the Decisions server URL used by New and NewSecurity
// when no endpoint is set. Tests redirect it to an httptest server; it stays
// a package variable rather than a constant so pipeline-level tests can point
// the guardrail at a stub without a config field.
var DefaultServerURL = decisionsServerURL

// IsDecisionsModel reports whether a provider/model pair is a Jev Decisions
// model: the openrouter provider serving a typesafe/jev* model. It is the
// single predicate that routes Jev traffic to the Decisions API and keeps Jev
// away from chat/completions.
func IsDecisionsModel(provider, model string) bool {
	return provider == "openrouter" && strings.HasPrefix(model, "typesafe/jev")
}

// BuildPayload constructs the gate request for one tool call. Tools, Skills,
// and Agent are always empty: the gate turn must never expose tools, skills,
// or an agent block. User carries the tool call (intent + arguments) that Jev
// evaluates as the Decisions request's state.
func BuildPayload(toolCall string) rolemanager.ClassifierPayload {
	return rolemanager.ClassifierPayload{User: toolCall}
}

// noulQuestion is one boolean ("noul") question in a Decisions response. It is
// the local response shape ParseAnswer and ParseRouteAnswer read; the wire
// requests use the OpenRouter SDK's components types.
type noulQuestion struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
}

// decisionsRequest is the OpenRouter Decisions API request body (retained for
// the local parsing helpers; live requests go through the SDK).
type decisionsRequest struct {
	Model     string                  `json:"model"`
	Questions map[string]noulQuestion `json:"questions"`
	State     string                  `json:"state"`
}

// noulAnswer is one boolean answer in a Decisions response.
type noulAnswer struct {
	Type string  `json:"type"`
	Noul float64 `json:"noul"`
}

// decisionsResponse is the OpenRouter Decisions API response body (the fields
// the gate reads).
type decisionsResponse struct {
	Model   string                `json:"model"`
	Answers map[string]noulAnswer `json:"answers"`
}

// Client implements rolemanager.Classifier for the tool-call gate: it sends a
// Decisions request to the Jev model and thresholds the returned probability.
type Client struct {
	client   *http.Client
	token    func() (string, error)
	model    string
	endpoint string
}

// New wraps an OpenRouter API-key resolver as a Jev tool-call gate. token is
// called once per Classify so a key fetched lazily (netrc, keychain) stays
// fresh.
func New(token func() (string, error)) *Client {
	return &Client{
		client:   &http.Client{Timeout: 30 * time.Second},
		token:    token,
		model:    DefaultModel,
		endpoint: DefaultServerURL,
	}
}

// SetEndpoint overrides the Decisions server URL (test seam). The SDK appends
// /api/alpha/decisions to it.
func (c *Client) SetEndpoint(endpoint string) { c.endpoint = endpoint }

// decisionsSDK builds an OpenRouter SDK client for one Decisions call. The
// security source resolves the token lazily so a per-call resolver stays
// fresh, and the custom HTTP client carries the package timeout.
func (c *Client) decisionsSDK() *openrouter.OpenRouter {
	return openrouter.New(
		openrouter.WithClient(c.client),
		openrouter.WithSecuritySource(func(context.Context) (components.Security, error) {
			token, err := c.token()
			if err != nil {
				return components.Security{}, err
			}
			return components.Security{APIKey: &token}, nil
		}),
	)
}

// noulOf reads one noul answer from an SDK response. A missing answer, a
// non-noul answer, or a probability outside [0,1] is an error — a broken
// check must never become an approval or a review.
func noulOf(answers map[string]components.Answers, key string) (float64, error) {
	ans, ok := answers[key]
	if !ok || ans.DecisionsNoulAnswer == nil {
		return 0, fmt.Errorf("jev decisions response: missing %s answer", key)
	}
	n := ans.DecisionsNoulAnswer.Noul
	if n < 0 || n > 1 {
		return 0, fmt.Errorf("jev decisions response: noul out of range %.3f", n)
	}
	return n, nil
}

// Classify implements rolemanager.Classifier. A transport or non-2xx error
// from the Decisions API is an error; a reply that cannot be parsed as a
// probability is INCONCLUSIVE, not an error, so the caller can fail closed.
func (c *Client) Classify(ctx context.Context, p rolemanager.ClassifierPayload) (string, error) {
	req := components.DecisionsRequest{
		Model: c.model,
		Questions: map[string]components.Questions{
			unsafeQuestion: components.CreateQuestionsNoul(components.DecisionsNoulQuestion{
				Instructions: components.CreateDecisionsNoulQuestionInstructionsStr(unsafeInstruction),
			}),
		},
		State: components.CreateStateMapOfAny(map[string]any{"tool_call": p.User}),
	}
	resp, err := c.decisionsSDK().Alpha.Decisions.Create(ctx, req, operations.WithServerURL(c.endpoint))
	if err != nil {
		return "", err
	}
	n, err := noulOf(resp.Answers, unsafeQuestion)
	if err != nil {
		// A malformed answer is inconclusive, not a block and not an error:
		// the caller fails closed on INCONCLUSIVE.
		return string(Inconclusive), nil
	}
	return string(Threshold(n)), nil
}

// Threshold maps a noul probability to a Sentinel. At or above denyAt the
// proposition is true (Deny); at or below allowAt it is false (Allow); in
// between the gate cannot decide (Inconclusive).
func Threshold(prob float64) Sentinel {
	if prob >= denyAt {
		return Deny
	}
	if prob <= allowAt {
		return Allow
	}
	return Inconclusive
}

// ParseAnswer parses a Decisions API response body and returns the thresholded
// verdict for the unsafe question. A malformed body, a missing answer, or a
// probability outside [0,1] is an error — a broken check must never become an
// approval or a review.
func ParseAnswer(raw []byte) (Sentinel, error) {
	var out decisionsResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("malformed jev decisions response: %w", err)
	}
	ans, ok := out.Answers[unsafeQuestion]
	if !ok || ans.Type != "noul" {
		return "", fmt.Errorf("jev decisions response: missing unsafe answer")
	}
	if ans.Noul < 0 || ans.Noul > 1 {
		return "", fmt.Errorf("jev decisions response: noul out of range %.3f", ans.Noul)
	}
	return Threshold(ans.Noul), nil
}

// Candidate is one provider/model pair the Jev router may select for a use
// case. Key is the use-case entry key (e.g. "mode_eval") and is echoed back in
// RouteDecision.Key.
type Candidate struct {
	Key         string
	Provider    string
	Model       string
	Description string // optional context appended to the proposition
}

// RouteDecision is the routing verdict for one use case.
type RouteDecision struct {
	// Key is the chosen candidate key. Empty means inconclusive; the caller
	// falls back to the defined global provider/model.
	Key    string
	Scores map[string]float64 // noul probability per candidate key
}

// routeThreshold is the noul probability a candidate must exceed to be
// selected. At or below it the answer is "probably not this candidate".
const routeThreshold = 0.5

// Route asks Jev which of candidates should serve useCase. It sends one noul
// question per candidate — "This use case should be served by <provider>
// <model>." — and selects the unique candidate with the highest noul above
// 0.5. A tie at the top, or no score above 0.5, is inconclusive (empty Key).
// A transport or non-2xx error is returned; a malformed reply is inconclusive,
// not an error, so the caller can fail closed to the defined global model.
func (c *Client) Route(ctx context.Context, useCase string, candidates []Candidate) (RouteDecision, error) {
	if len(candidates) == 0 {
		return RouteDecision{}, fmt.Errorf("jev route: no candidates")
	}
	questions := make(map[string]components.Questions, len(candidates))
	for _, cand := range candidates {
		if strings.TrimSpace(cand.Key) == "" {
			return RouteDecision{}, fmt.Errorf("jev route: empty candidate key")
		}
		questions[cand.Key] = components.CreateQuestionsNoul(components.DecisionsNoulQuestion{
			Instructions: components.CreateDecisionsNoulQuestionInstructionsStr(routeInstruction(cand)),
		})
	}
	req := components.DecisionsRequest{
		Model:     c.model,
		Questions: questions,
		State:     components.CreateStateMapOfAny(map[string]any{"use_case": useCase}),
	}
	resp, err := c.decisionsSDK().Alpha.Decisions.Create(ctx, req, operations.WithServerURL(c.endpoint))
	if err != nil {
		return RouteDecision{}, err
	}
	scores, err := routeAnswerScores(resp.Answers, candidates)
	if err != nil {
		// A malformed answer is inconclusive, not an error: the caller fails
		// closed to the defined global model.
		return RouteDecision{Scores: map[string]float64{}}, nil
	}
	return RouteDecision{Key: SelectRoute(scores), Scores: scores}, nil
}

// routeAnswerScores reads one noul answer per candidate. A missing answer or a
// probability outside [0,1] is an error, so a broken reply can never silently
// select a candidate.
func routeAnswerScores(answers map[string]components.Answers, candidates []Candidate) (map[string]float64, error) {
	scores := make(map[string]float64, len(candidates))
	for _, cand := range candidates {
		n, err := noulOf(answers, cand.Key)
		if err != nil {
			return nil, err
		}
		scores[cand.Key] = n
	}
	return scores, nil
}

// routeInstruction renders the proposition Jev evaluates for one candidate.
func routeInstruction(cand Candidate) string {
	base := fmt.Sprintf("This use case should be served by %s %s.", cand.Provider, cand.Model)
	if cand.Description != "" {
		base += " " + cand.Description
	}
	return base
}

// ParseRouteAnswer parses a Decisions response with one answer per candidate
// key. A malformed body, a missing answer, or a probability outside [0,1] is
// an error, so a broken reply can never silently select a candidate.
func ParseRouteAnswer(raw []byte, candidates []Candidate) (map[string]float64, error) {
	var out decisionsResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("malformed jev route response: %w", err)
	}
	scores := make(map[string]float64, len(candidates))
	for _, cand := range candidates {
		ans, ok := out.Answers[cand.Key]
		if !ok || ans.Type != "noul" {
			return nil, fmt.Errorf("jev route response: missing answer for %q", cand.Key)
		}
		if ans.Noul < 0 || ans.Noul > 1 {
			return nil, fmt.Errorf("jev route response: noul out of range %.3f for %q", ans.Noul, cand.Key)
		}
		scores[cand.Key] = ans.Noul
	}
	return scores, nil
}

// SelectRoute picks the winning candidate from the raw noul scores. It
// returns the empty string when no candidate exceeds routeThreshold, or when
// the top score is tied: a routing decision must be a single clear winner, and
// anything else falls back to the defined global model.
func SelectRoute(scores map[string]float64) string {
	best := ""
	bestScore := 0.0
	tie := false
	for key, score := range scores {
		switch {
		case score > bestScore:
			best = key
			bestScore = score
			tie = false
		case score == bestScore && score > 0:
			tie = true
		}
	}
	if best == "" || bestScore <= routeThreshold || tie {
		return ""
	}
	return best
}

// truncate bounds an error body to a short excerpt.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Security is a Jev security classifier: one Decisions request carrying one
// noul question per threat category in the payload, thresholded to a plain
// sentinel token. A high probability on any category returns the
// highest-scoring sentinel; all-low returns SAFE; the band between — or a
// malformed or missing answer — hands off to the fallback classifier, which
// is the agent model answering the unchanged chat payload. A transport or
// non-2xx error is returned as an error so the pipeline fails closed.
type Security struct {
	client   *http.Client
	token    func() (string, error)
	model    string
	endpoint string
	fallback rolemanager.Classifier
}

// NewSecurity wraps an OpenRouter API-key resolver and a fallback classifier
// into a Jev security classifier. token is called once per Classify; fallback
// is the agent model serving the unchanged chat payload on inconclusive or
// malformed Decisions answers.
func NewSecurity(token func() (string, error), fallback rolemanager.Classifier) *Security {
	return &Security{
		client:   &http.Client{Timeout: 30 * time.Second},
		token:    token,
		model:    DefaultModel,
		endpoint: DefaultServerURL,
		fallback: fallback,
	}
}

// SetEndpoint overrides the Decisions server URL (test seam).
func (s *Security) SetEndpoint(endpoint string) { s.endpoint = endpoint }

// decisionsSDK builds an OpenRouter SDK client for one Decisions call.
func (s *Security) decisionsSDK() *openrouter.OpenRouter {
	return openrouter.New(
		openrouter.WithClient(s.client),
		openrouter.WithSecuritySource(func(context.Context) (components.Security, error) {
			token, err := s.token()
			if err != nil {
				return components.Security{}, err
			}
			return components.Security{APIKey: &token}, nil
		}),
	)
}

// Classify implements rolemanager.Classifier. The reply is a plain sentinel
// token, so ParseSentinel, ParseExtractionSentinel, the chunked path and
// mlclassify phase 3 all read it unchanged.
func (s *Security) Classify(ctx context.Context, p rolemanager.ClassifierPayload) (string, error) {
	if len(p.Categories) == 0 {
		return s.fallback.Classify(ctx, p)
	}
	questions := make(map[string]components.Questions, len(p.Categories))
	for _, cat := range p.Categories {
		questions[string(cat)] = components.CreateQuestionsNoul(components.DecisionsNoulQuestion{
			Instructions: components.CreateDecisionsNoulQuestionInstructionsStr(categoryProposition(cat)),
		})
	}
	req := components.DecisionsRequest{
		Model:     s.model,
		Questions: questions,
		State:     components.CreateStateMapOfAny(map[string]any{"content": p.User}),
	}
	resp, err := s.decisionsSDK().Alpha.Decisions.Create(ctx, req, operations.WithServerURL(s.endpoint))
	if err != nil {
		return "", err
	}
	verdict, decided := thresholdCategories(resp.Answers, p.Categories)
	if decided {
		return verdict, nil
	}
	// Inconclusive: record the handoff so the TUI shows the agent model ruled,
	// then answer the unchanged chat payload with the fallback classifier.
	rolemanager.RecordSecurityFallback()
	return s.fallback.Classify(ctx, p)
}

// thresholdCategories reduces the per-category noul answers to a verdict. A
// missing answer, a non-noul answer, or a probability outside [0,1] makes the
// result undecided. Any category at or above denyAt returns the
// highest-scoring sentinel; every category at or below allowAt returns SAFE;
// the band between is undecided.
func thresholdCategories(answers map[string]components.Answers, categories []rolemanager.Sentinel) (string, bool) {
	best := ""
	bestScore := 0.0
	allLow := true
	for _, cat := range categories {
		ans, ok := answers[string(cat)]
		if !ok || ans.DecisionsNoulAnswer == nil {
			return "", false
		}
		n := ans.DecisionsNoulAnswer.Noul
		if n < 0 || n > 1 {
			return "", false
		}
		if n > allowAt {
			allLow = false
		}
		if n > bestScore {
			best = string(cat)
			bestScore = n
		}
	}
	if bestScore >= denyAt {
		return best, true
	}
	if allLow {
		return string(rolemanager.SentinelSafe), true
	}
	return "", false
}

// categoryPropositions are the fixed propositions Jev evaluates per threat
// category. A high noul probability means the proposition is true (the
// content attempts that category).
var categoryPropositions = map[rolemanager.Sentinel]string{
	rolemanager.SentinelPromptInjection: "The content attempts prompt injection against an LLM.",
	rolemanager.SentinelJailbreak:       "The content attempts a jailbreak or safety override.",
	rolemanager.SentinelDataExtraction:  "The content attempts training-data extraction or membership inference.",
	rolemanager.SentinelModelExtraction: "The content attempts model extraction or model stealing.",
}

// categoryProposition returns the fixed proposition for a threat category,
// falling back to a generic unsafe proposition for unknown categories.
func categoryProposition(cat rolemanager.Sentinel) string {
	if p, ok := categoryPropositions[cat]; ok {
		return p
	}
	return "The content is unsafe."
}
