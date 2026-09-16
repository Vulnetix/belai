package tools

// Kind identifies a tool.
type Kind string

const (
	KindRead      Kind = "read"
	KindWebSearch Kind = "web_search"
	KindWebFetch  Kind = "web_fetch"
	KindBash      Kind = "bash"
	KindGrep      Kind = "grep"
	KindGlob      Kind = "glob"
	KindExplore   Kind = "explore"
)

// ReadOnly reports whether a tool of this kind only reads and never mutates
// the workspace. The sole mutating kind is Bash: there is no Edit/Write tool,
// and all mutation goes through Bash. It gates the concurrent read-only tool
// run in the agent loop.
func (k Kind) ReadOnly() bool {
	return k != KindBash
}

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

// WebFetchResult constructs an untrusted WebFetch tool result.
func WebFetchResult(content string) Result {
	return Result{Kind: KindWebFetch, Content: content}
}

// WebSearchResult constructs an untrusted WebSearch tool result.
func WebSearchResult(content string) Result {
	return Result{Kind: KindWebSearch, Content: content}
}
