package tools

import "context"

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

// Progress is a chunk of tool output reported while the tool is still running.
// Text holds one or more whole lines with no trailing newline: partial lines
// are held back until they complete, so a consumer never has to reassemble
// them. Stream names the origin for display only — ordering is already correct
// and the two streams are interleaved as the process wrote them.
//
// Progress is render-only. It never reaches a model, and nothing downstream
// may treat it as tool output: the authoritative result is the Result the tool
// eventually returns.
type Progress struct {
	Stream string // "stdout" or "stderr"
	Text   string
}

// Sink receives Progress as it is produced. It may be called from a goroutine
// other than the one that called ExecuteStream, and it may block — blocking is
// how backpressure reaches the subprocess.
type Sink func(Progress)

// StreamingTool is a Tool that can report its output as it is produced rather
// than only when it finishes.
//
// Execute must remain equivalent to ExecuteStream with a nil sink, so a caller
// that does not care about progress can ignore this interface entirely.
type StreamingTool interface {
	Tool
	ExecuteStream(ctx context.Context, args map[string]any, sink Sink) (Result, error)
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
