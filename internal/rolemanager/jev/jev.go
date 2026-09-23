// Package jev implements the Role Manager Jev tool-call gate: a classifier
// that asks the TypeSafe/Jev decision model on OpenRouter's Decisions API
// whether a model-emitted tool call is safe, and reduces the returned
// probability to a strict single-token verdict. The gate turn carries no
// tools, skills, or agent block.
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

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

	// decisionsEndpoint is the OpenRouter Decisions API endpoint.
	decisionsEndpoint = "https://openrouter.ai/api/alpha/decisions"

	// unsafeQuestion keys the single noul question the gate asks. Its
	// probability is the probability that the proposition is true.
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

// BuildPayload constructs the gate request for one tool call. Tools, Skills,
// and Agent are always empty: the gate turn must never expose tools, skills,
// or an agent block. User carries the tool call (intent + arguments) that Jev
// evaluates as the Decisions request's state.
func BuildPayload(toolCall string) rolemanager.ClassifierPayload {
	return rolemanager.ClassifierPayload{User: toolCall}
}

// noulQuestion is one boolean ("noul") question in a Decisions request.
type noulQuestion struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
}

// decisionsRequest is the OpenRouter Decisions API request body.
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

// Client implements rolemanager.Classifier by sending a Decisions request to
// the Jev model and thresholding the returned probability.
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
		endpoint: decisionsEndpoint,
	}
}

// Classify implements rolemanager.Classifier. A transport error from the
// Decisions API is an error; a reply that cannot be parsed as a probability is
// INCONCLUSIVE, not an error, so the caller can fail closed.
func (c *Client) Classify(ctx context.Context, p rolemanager.ClassifierPayload) (string, error) {
	token, err := c.token()
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(decisionsRequest{
		Model: c.model,
		Questions: map[string]noulQuestion{
			unsafeQuestion: {Type: "noul", Instructions: unsafeInstruction},
		},
		State: p.User,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("jev decisions %s: %s", resp.Status, truncate(string(data), 200))
	}
	s, err := ParseAnswer(data)
	if err != nil {
		// A malformed answer is inconclusive, not a block and not an error:
		// the caller fails closed on INCONCLUSIVE.
		return string(Inconclusive), nil
	}
	return string(s), nil
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

// truncate bounds an error body to a short excerpt.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
