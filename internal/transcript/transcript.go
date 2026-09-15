// Package transcript models a provider-neutral conversation and provides
// hybrid context accounting plus a safe serializer. It is a leaf package with
// no dependencies on the TUI or wire layers.
package transcript

import "unicode/utf8"

// Usage is provider-reported token usage for one assistant response.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// Total returns TotalTokens, falling back to Prompt+Completion when the
// provider reports only the components (Anthropic does).
func (u Usage) Total() int {
	if u.TotalTokens != 0 {
		return u.TotalTokens
	}
	return u.PromptTokens + u.CompletionTokens
}

// Message is one transcript entry in provider-neutral form.
type Message struct {
	Role     string // "user" | "assistant" | "tool" | "system"
	Content  string
	ToolName string
	Usage    *Usage // non-nil only on assistant messages the provider metered
}

// EstimateTokens approximates one message at ~4 characters per token, counting
// runes so multi-byte text is not double-counted. Conservative by design:
// overestimating context pressure is the safe direction to be wrong in.
func EstimateTokens(m Message) int {
	const roleOverhead = 16
	return (utf8.RuneCountInString(m.Content) + roleOverhead + 3) / 4
}
