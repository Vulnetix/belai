// Package tools defines tool-result types. Outputs from Read, WebSearch, and
// WebFetch are untrusted: they must pass through the Role Manager pipeline
// before they may be promoted into trusted context.
package tools

// Kind identifies a tool.
type Kind string

const (
	KindRead      Kind = "read"
	KindWebSearch Kind = "web_search"
	KindWebFetch  Kind = "web_fetch"
)

// Result is a tool output.
type Result struct {
	Kind    Kind
	Content string
	Meta    map[string]any
}

// ReadResult constructs an untrusted Read tool result.
func ReadResult(content string) Result {
	return Result{Kind: KindRead, Content: content}
}
