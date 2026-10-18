// Package planest estimates what approving a plan re-sends to the model. An
// approved plan is written into the system block (prompt.CarrierPlan), so the
// first execute request is sent without a cache hit whichever model runs it:
// approving here re-sends the planning history plus the plan, approving in a
// new session sends only the system prompt, the tool definitions and the plan.
//
// The figures are the harness's usual chars/4 estimates
// (transcript.EstimateTokens), not a tokenizer's count, and are shown as
// approximate.
package planest

import "github.com/vulnetix/belai/internal/transcript"

// ExecutePrompt is the user turn that starts an approved plan's execution.
const ExecutePrompt = "Execute the approved plan."

// Estimate is the approximate size, in tokens, of each approve route's first
// request.
type Estimate struct {
	// Plan is the plan text, which becomes part of the system prompt.
	Plan int `json:"plan"`
	// Base is the system prompt and tool definitions without the plan, from
	// the last request; 0 when no request has been made yet.
	Base int `json:"base,omitempty"`
	// Here is approving in this session: the history (which already counts
	// the last system prompt and tools), the plan and the execute prompt.
	// 0 when the history is unknown.
	Here int `json:"here,omitempty"`
	// New is approving in a new session: Base, the plan and the execute
	// prompt.
	New int `json:"new"`
}

// Compute estimates both routes. baseTokens is the last request's system
// prompt and tool definitions; historyTokens is the session's context estimate
// (transcript.EstimateContext), or 0 when unknown, which leaves Here unset.
func Compute(planText string, baseTokens, historyTokens int) Estimate {
	plan := transcript.EstimateTokens(transcript.Message{Role: "system", Content: planText})
	exec := transcript.EstimateTokens(transcript.Message{Role: "user", Content: ExecutePrompt})
	e := Estimate{Plan: plan, Base: max(baseTokens, 0), New: max(baseTokens, 0) + plan + exec}
	if historyTokens > 0 {
		e.Here = historyTokens + plan + exec
	}
	return e
}

// Share is tokens as a whole percentage of window, rounded down, and whether
// it fits. An unknown window (<= 0) reports 0 and fits, so nothing is flagged
// on a guess.
func Share(tokens, window int) (pct int, fits bool) {
	if window <= 0 || tokens <= 0 {
		return 0, true
	}
	pct = tokens * 100 / window
	return pct, tokens <= window
}
